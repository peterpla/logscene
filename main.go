package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/bits"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // embed IANA timezone database for Windows

	"github.com/c2h5oh/datasize"
	"github.com/go-playground/validator"
	"github.com/julienschmidt/httprouter"
	"github.com/monoculum/formam"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

var srv *server

var (
	apiHTTPClient   = &http.Client{Timeout: 2 * time.Second}
	imageHTTPClient = &http.Client{Timeout: 10 * time.Second}
)

var currentLogFile *os.File

// openLogFile opens (or creates) a dated log file in the configured log
// directory, redirects the standard logger to it, and closes the previous file.
func openLogFile(date time.Time) error {
	if err := os.MkdirAll(srv.config.logDir, 0755); err != nil {
		return fmt.Errorf("openLogFile: MkdirAll %s: %w", srv.config.logDir, err)
	}
	filename := filepath.Join(srv.config.logDir, "timelapse-"+date.Format("2006-01-02")+".log")
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("openLogFile: %w", err)
	}
	log.SetOutput(f)
	if currentLogFile != nil {
		currentLogFile.Close()
	}
	currentLogFile = f
	log.Printf("openLogFile: logging to %s", filename)
	return nil
}

// newDayMaintenance wakes at midnight each day to rotate the log file.
// It is the intended hook for any future daily maintenance tasks.
func newDayMaintenance(ctx context.Context) {
	sn := "newDayMaintenance"
	for {
		now := time.Now().In(srv.localLoc)
		nextMidnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 1, 0, srv.localLoc)
		select {
		case <-time.After(time.Until(nextMidnight)):
		case <-ctx.Done():
			log.Printf("%s exiting after ctx.Done\n", sn)
			srv.wg.Done()
			return
		}

		if err := openLogFile(time.Now().In(srv.localLoc)); err != nil {
			log.Printf("%s, openLogFile: %v\n", sn, err)
		}
	}
}

const (
	masterFile = "timelapse.json"
	timeLayout = "2006-01-02T15:04:05Z" // ISO 8601; see https://sunrise-sunset.org/api, https://godoc.org/time#Time.Format and https://ednsquare.com/story/date-and-time-manipulation-golang-with-examples------cU1FjK
)

const (
	firstSunrise uint = 1 << iota
	firstSunrise30
	firstSunrise60
	firstTime
)

const (
	lastSunset uint = 1 << iota
	lastSunset30
	lastSunset60
	lastTime
)

func main() {
	var err error

	defer catch() // implements recover so panics reported
	sn := "main"

	srv = newServer()

	if err = srv.mtld.Read(filepath.Join(srv.config.path, masterFile)); err != nil {
		msg := fmt.Sprintf("%s, srv.mtld.Read: %v", sn, err)
		panic(msg)
	}

	if err = openLogFile(time.Now().In(srv.localLoc)); err != nil {
		log.Printf("%s, openLogFile: %v (continuing with stderr)\n", sn, err)
	}

	// use context and cancel with goroutines to handle Ctrl+C
	// TODO: add ctx to server struct, so availble when adding a new webcam and launching new go routine
	srv.ctx, srv.cancel = context.WithCancel(context.Background())

	srv.wg.Add(1)
	go newDayMaintenance(srv.ctx)

	for _, tld := range *(srv.mtld) {
		// log.Printf("%s, launching goroutine #%d (%s), FirstFlags %b, LastFlags %b",
		// 	sn, i, tld.Name, tld.FirstFlags, tld.LastFlags)
		srv.wg.Add(1)
		go capture(srv.ctx, tld, srv.config.pollSecs)
		time.Sleep(2 * time.Second) // respect TimeZoneDB.com limit 1 request/second
	}

	srv.initTemplates("./templates", ".html")
	srv.router.ServeFiles("/static/*filepath", http.Dir("static")) // TODO: assemble path to avoid platform-specific path separator issues
	srv.router.POST("/new", srv.handleNew())
	srv.router.GET("/", srv.handleHome())
	srv.router.GET("/status", srv.handleStatus())
	srv.router.GET("/next", srv.handleNext())

	hs := http.Server{
		Addr:         ":" + srv.config.port,
		Handler:      logReqResp(srv.router),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	log.Printf("Starting service %s listening on port %s", sn, hs.Addr)
	go startListening(&hs, "main") // call ListenAndServe from a separate go routine so main can listen for signals
	go printStartupSummary()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	defer func() { // on handled signals, cancel goroutines, wait and exit
		signal.Stop(c)
		srv.cancel()
		srv.wg.Wait()
	}()

	s := <-c
	log.Printf("\n%s received signal %s, terminating", sn, s.String())
}

// startListening invokes ListenAndServe
func startListening(hs *http.Server, sn string) {
	if err := hs.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("%s startListening, ListenAndServe returned err: %+v\n", sn, err)
	}
}

// logReqResp is a minimal HTTP middleware that logs each request method, path,
// and elapsed time, replacing the former lead-expert/pkg/middleware dependency.
func logReqResp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %v", r.Method, r.RequestURI, time.Since(start).Truncate(time.Millisecond))
	})
}

// catch uses recover() and logs it
func catch() {
	sn := "main"
	defer func() {
		if r := recover(); r != nil {
			log.Fatalf("=====> RECOVER in %s.catch, recover() returned: %v\n", sn, r)
		}
	}()
}

// printStartupSummary waits for capture goroutines to finish initializing
// then prints /status and /next to stdout so the user gets confirmation
// in the terminal even though log output has been redirected to a file.
func printStartupSummary() {
	time.Sleep(3 * time.Second) // allow last capture goroutine to finish initializing

	client := &http.Client{Timeout: 5 * time.Second}
	base := "http://localhost:" + srv.config.port

	for _, path := range []string{"/status", "/next"} {
		resp, err := client.Get(base + path)
		if err != nil {
			fmt.Fprintf(os.Stdout, "startup %s: %v\n", path, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		fmt.Fprintf(os.Stdout, "%s\n", strings.TrimSpace(string(body)))
	}
}

// ********** ********** ********** ********** ********** **********

func capture(ctx context.Context, tld *TLDef, pollInterval int) {
	sn := fmt.Sprintf("capture.%s", tld.Name)

	if err := tld.SetCaptureTimes(ctx, time.Now()); err != nil {
		log.Printf("%s, SetCaptureTimes: %v, exiting\n", sn, err)
		srv.wg.Done()
		return
	}
	if err := tld.UpdateNextCapture(ctx, time.Now()); err != nil {
		log.Printf("%s, UpdateNextCapture: %v, exiting\n", sn, err)
		srv.wg.Done()
		return
	}

	log.Printf("%s, timezone %s, NextCapture %s, CaptureTimes (len %d): %v, FirstFlags %b, LastFlags %b\n",
		sn, tld.WebcamTZ, tld.CaptureTimes[tld.NextCapture], len(tld.CaptureTimes), tld.CaptureTimes, tld.FirstFlags, tld.LastFlags)

	for {
		select {
		case <-ctx.Done():
			log.Printf("%s exiting after ctx.Done\n", sn)
			srv.wg.Done()
			return
		default:
			if tld.IsTimeForCapture() {
				if tld.Backoff > 0 {
					log.Printf("%s, backing off %v\n", sn, time.Duration(tld.Backoff))
					time.Sleep(time.Duration(tld.Backoff))
				}

				createdName, createdSize, err := tld.CaptureImage(ctx)
				if err != nil {
					log.Printf("%s, CaptureImage: %v\n", sn, err)
					tld.AdjustBackoff()
					break
				}
				tld.Backoff = 0 // after successful capture, no backoff
				log.Printf("%s, %s created, size %s", sn, createdName, datasize.ByteSize(createdSize).HumanReadable())

				if err := tld.UpdateNextCapture(ctx, time.Now()); err != nil {
					log.Printf("%s, UpdateNextCapture: %v, exiting\n", sn, err)
					srv.wg.Done()
					return
				}
			}
		}
		// log.Printf("%s sleeping for %d seconds...\n", sn, pollInterval)
		time.Sleep(time.Duration(pollInterval) * time.Second)
	}
}

// AdjustBackoff implements our backoff policy when cannot retrieve a webcam image
func (tld *TLDef) AdjustBackoff() {
	const maxBackoff = time.Minute * 10

	if tld.Backoff == 0 {
		tld.Backoff = int64(time.Second)
	} else {
		tld.Backoff *= 2
	}
	if time.Duration(tld.Backoff) > maxBackoff {
		tld.Backoff = int64(maxBackoff)
	}
}

// CaptureImage retrieves the webcam image and saves it in the specified
// folder. The image is fetched before the file is created so that HTTP
// errors never leave an empty file on disk.
func (tld *TLDef) CaptureImage(ctx context.Context) (string, int64, error) {
	respBody, contentType, err := tld.RetrieveImage(ctx)
	if err != nil {
		return "", 0, err
	}
	defer respBody.Close()

	fileName := tld.TargetFileName() + extensionForContentType(contentType)
	newFile, err := os.Create(fileName)
	if err != nil {
		return "", 0, err
	}
	defer newFile.Close()

	written, err := io.Copy(newFile, respBody) // io.Copy buffers I/O to support huge files
	if err != nil {
		os.Remove(fileName) // discard partial file on copy failure
		return "", 0, err
	}

	return newFile.Name(), written, nil
}

// extensionForContentType returns the file extension for a MIME type
func extensionForContentType(contentType string) string {
	ct := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	switch ct {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".jpg" // safe default for webcam images
	}
}

// TargetFileName returns the full target path, appending the capture
// date and time to the webcam name, e.g., "[folder]/Manzanita Lake YYYYMMddhhmmss"
func (tld *TLDef) TargetFileName() string {
	layout := "20060102150405"
	captureDateTime := tld.CaptureTimes[tld.NextCapture]
	// log.Print("TODO: format tld.CaptureTimes[NextCapture] into YYYYMMDDhhmmss")
	fileName := tld.Name + " " + captureDateTime.Format(layout)
	return filepath.Join(tld.FolderPath, fileName)
}

// RetrieveImage retrieves the webcam image and returns resp.Body and
// Content-Type for reading and closing by the caller
func (tld *TLDef) RetrieveImage(ctx context.Context) (io.ReadCloser, string, error) {
	// sn := fmt.Sprintf("RetrieveImage.%q", tld.Name)

	webcamReq, err := http.NewRequestWithContext(ctx, "GET", tld.URL, nil)
	if err != nil {
		// log.Printf("%s http.NewRequest: %v\n", sn, err)
		return nil, "", err
	}

	resp, err := imageHTTPClient.Do(webcamReq)
	if err != nil {
		// log.Printf("%s client.Do: %v\n", sn, err)
		return nil, "", err
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", fmt.Errorf("RetrieveImage %s: unexpected status %s", tld.Name, resp.Status)
	}

	return resp.Body, resp.Header.Get("Content-Type"), nil
}

// ********** ********** ********** ********** ********** **********

type server struct {
	router    *httprouter.Router
	validate  *validator.Validate // use a single instance of Validate, it caches struct info
	config    *Config
	tmpl      *template.Template
	localLoc  *time.Location  // timezone where this code is running
	mtld      *masterTLDefs   // timelapse definitions, read from/written to timelapse.json
	ctx       context.Context // context used to cancel go routines
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	mu        sync.RWMutex // protects mtld slice and per-TLDef CaptureTimes/NextCapture
	startTime time.Time
}

// newServer creates a new instance of server with router and validation
// initialized and application configuration loaded
func newServer() *server {
	var err error
	sn := "newServer"

	s := &server{}
	s.router = httprouter.New()
	s.validate = validator.New()

	s.localLoc, err = time.LoadLocation("Local")
	if err != nil {
		msg := fmt.Sprintf("%s, time.LoadLocation(\"Local\"): %v", sn, err)
		panic(msg)
	}

	s.mtld = newMasterTLDefs()
	s.startTime = time.Now()

	s.config = &Config{}
	s.config.Load()

	return s
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// handleHome is the handler for "/"
func (s *server) handleHome() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		// startTime := time.Now()

		data := struct {
			Company string
		}{
			Company: "Timelapse",
		}

		srv.tmpl.ExecuteTemplate(w, "layout", data)

		// log.Printf("%s.%s, duration %v\n", sn, mn, time.Now().Sub(startTime))
	}
}

// handlenew is the handler for webform submissions with new TLDef specifications
func (s *server) handleNew() httprouter.Handle {

	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		sn := "handleNew"
		// startTime := time.Now()

		if err := r.ParseForm(); err != nil {
			log.Printf("FromForm: r.ParseForm: %v *****ERROR*****\n", err)
			return
		}

		// dump r.Form contents
		log.Println("After r.ParseForm(), r.Form values:")
		for key, value := range r.Form {
			log.Printf("%q: %q\n", key, value)
		}

		tld := newTLDef()

		decoder := formam.NewDecoder(nil)
		if err := decoder.Decode(r.Form, tld); err != nil {
			log.Printf("%s, decoder.Decode: %v\n", sn, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// validate the TLDef we just decoded
		if err := srv.validate.Struct(tld); err != nil {
			log.Printf("%s, handleNew: %v\n", sn, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// validator package doesn't allow required numbers to be zero, so validate manually
		if _, ok := r.Form["additional"]; !ok { // additional not present
			msg := "Additional is required"
			log.Printf("%s, handleNew: %s\n", sn, msg)
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if _, ok := r.Form["additional"]; ok {
			formVal := r.Form["additional"]
			value, err := strconv.Atoi(formVal[0])
			if err != nil {
				log.Printf("%s, handleNew: strconv.Atoi(%s) %s\n", sn, formVal, err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if value < 0 || value > 16 {
				msg := "Additional must be 0-16"
				log.Printf("%s, handleNew: %s\n", sn, msg)
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
		}

		// process checkbox values
		if _, ok := r.Form["firstTime"]; ok {
			tld.FirstTime = true
		}
		if _, ok := r.Form["firstSunrise"]; ok {
			tld.FirstSunrise = true
		}
		if _, ok := r.Form["firstSunrise30"]; ok {
			tld.FirstSunrise30 = true
		}
		if _, ok := r.Form["firstSunrise60"]; ok {
			tld.FirstSunrise60 = true
		}
		if _, ok := r.Form["lastTime"]; ok {
			tld.LastTime = true
		}
		if _, ok := r.Form["lastSunset"]; ok {
			tld.LastSunset = true
		}
		if _, ok := r.Form["lastSunset30"]; ok {
			tld.LastSunset30 = true
		}
		if _, ok := r.Form["lastSunset60"]; ok {
			tld.LastSunset60 = true
		}

		if err := tld.SetFirstLastFlags(); err != nil {
			log.Printf("%s, SetFirstLastFlags: %v\n", sn, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// if the FolderPath directory doesn't exist, create it
		if err := os.MkdirAll(tld.FolderPath, 0664); err != nil { // octal for -rw-rw-r--: owner read/write, group/other read-only
			log.Printf("%s, os.MkdirAll: %v\n", sn, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("handleNew, TLDef: %+v", tld)

		srv.mu.Lock()
		srv.mtld.Append(tld)
		srv.mu.Unlock()
		if err := srv.mtld.Write(); err != nil {
			log.Printf("%s, srv.mtld.Write: %v\n", sn, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		srv.wg.Add(1)
		go capture(srv.ctx, tld, srv.config.pollSecs)

		http.Redirect(w, r, "/", http.StatusSeeOther)

		// log.Printf("%s.%s, duration %v\n", sn, mn, time.Now().Sub(startTime))
	}
}

// handleStatus returns JSON with app status, webcam count, and uptime
func (s *server) handleStatus() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		srv.mu.RLock()
		webcams := len(*srv.mtld)
		srv.mu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Status  string `json:"status"`
			Webcams int    `json:"webcams"`
			Uptime  string `json:"uptime"`
		}{
			Status:  "ok",
			Webcams: webcams,
			Uptime:  time.Since(srv.startTime).Truncate(time.Second).String(),
		})
	}
}

// handleNext returns JSON with the webcam name and time of the next scheduled capture
func (s *server) handleNext() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		srv.mu.RLock()
		var nextName string
		var nextTime time.Time
		for _, tld := range *srv.mtld {
			nc := tld.NextCapture
			ct := tld.CaptureTimes
			if nc >= len(ct) {
				continue
			}
			t := ct[nc]
			if nextTime.IsZero() || t.Before(nextTime) {
				nextTime = t
				nextName = tld.Name
			}
		}
		srv.mu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		if nextTime.IsZero() {
			json.NewEncoder(w).Encode(struct {
				Status string `json:"status"`
			}{"no captures scheduled"})
			return
		}
		json.NewEncoder(w).Encode(struct {
			Webcam      string `json:"webcam"`
			NextCapture string `json:"next_capture"`
			In          string `json:"in"`
		}{
			Webcam:      nextName,
			NextCapture: nextTime.Format(time.RFC3339),
			In:          time.Until(nextTime).Truncate(time.Second).String(),
		})
	}
}

// initTemplates reads and parses template files, and saves the template
// in the server receiver
func (s *server) initTemplates(dir string, ext string) {
	sn := "initTemplates"

	var allFiles []string
	files, err := os.ReadDir(dir)
	if err != nil {
		log.Fatalf("%s: os.ReadDir(%q): %v\n", sn, dir, err)
	}

	// all files in specified directory with specified extension treated as templates
	for _, file := range files {
		filename := file.Name()
		if strings.HasSuffix(filename, ext) {
			allFiles = append(allFiles, filepath.Join(dir, filename))
		}
	}

	s.tmpl = template.Must(template.ParseFiles(allFiles...)) // parses all .tmpl files in the 'templates' folder
}

// ********** ********** ********** ********** ********** **********

// Config holds application-wide configuration info
type Config struct {
	path     string // path to timelapse.json
	pollSecs int    // polling interval = delay to handle Ctrl-C
	port     string // TCP port to listen on
	tzdbAPI  string // API key for TimeZoneDB.com
	logDir   string // directory for daily log files
}

// Load populates Config with flag and environment variable values
func (c *Config) Load() {

	pflag.StringVar(&c.path, "path", "./", "path to folder containing timelapse.json") // TODO: assemble to avoid platform-specific path separator issues
	pflag.IntVar(&c.pollSecs, "poll", 60, "seconds between time checks")
	pflag.StringVar(&c.port, "port", "8099", "HTTP port to listen on")
	pflag.StringVar(&c.tzdbAPI, "tzdb", "", "API key for TimeZoneDB.com")
	pflag.StringVar(&c.logDir, "logdir", "./logs", "directory for daily log files")
	var help bool
	pflag.BoolVarP(&help, "help", "h", false, "show usage information")
	pflag.Parse()

	if help {
		pflag.PrintDefaults()
		os.Exit(0)
	}

	viper.BindPFlag("path", pflag.Lookup("path"))
	viper.BindPFlag("poll", pflag.Lookup("poll"))
	viper.BindPFlag("port", pflag.Lookup("port"))
	viper.BindPFlag("tzdb", pflag.Lookup("tzdb"))
	viper.BindPFlag("logdir", pflag.Lookup("logdir"))

	viper.SetEnvPrefix("timelapse")
	viper.AutomaticEnv()
	viper.BindEnv("path") // treats as upper-cased SetEnvPrefix value + "_" + upper-cased "path"
	viper.BindEnv("poll")
	viper.BindEnv("port")
	viper.BindEnv("tzdb")
	viper.BindEnv("logdir")

	c.path = viper.GetString("path")
	c.pollSecs = viper.GetInt("poll")
	c.port = viper.GetString("port")
	c.tzdbAPI = viper.GetString("tzdb")
	c.logDir = viper.GetString("logdir")

	log.Printf("Config: %+v\n", c)
}

// ********** ********** ********** ********** ********** **********

// CaptureTimes hold the series of webcam capture times
type CaptureTimes []time.Time

// TLDef represents a Timelapse capture definition
type TLDef struct {
	Name           string         `json:"name" formam:"name" validate:"required"`                     // Friendly name of this timelapse definition
	URL            string         `json:"webcamUrl" formam:"webcamUrl" validate:"url,required"`       // URL of webcam image
	Latitude       float64        `json:"latitude" formam:"latitude" validate:"latitude,required"`    // Latitude of webcam
	Longitude      float64        `json:"longitude" formam:"longitude" validate:"longitude,required"` // Longitude of webcam
	FirstTime      bool           `json:"firstTime" formam:"firstTime"`                               // First capture at specific time
	FirstSunrise   bool           `json:"firstSunrise" formam:"firstSunrise"`                         // First capture at Sunrise
	FirstSunrise30 bool           `json:"firstSunrise30" formam:"firstSunrise30"`                     // ................ Sunrise +30 minutes
	FirstSunrise60 bool           `json:"firstSunrise60" formam:"firstSunrise60"`                     // ................ Sunrise +60 minutes
	LastTime       bool           `json:"lastTime" formam:"lastTime"`                                 // Last capture at specific time
	LastSunset     bool           `json:"lastSunset" formam:"lastSunset"`                             // Last capture at Sunset
	LastSunset30   bool           `json:"lastSunset30" formam:"lastSunset30"`                         // ................ Sunset -30 minutes
	LastSunset60   bool           `json:"lastSunset60" formam:"lastSunset60"`                         // ................ Sunset -60 minutes
	Additional     int            `json:"additional" formam:"additional"`                             // Additional captures per day (in addition to First and Last)
	FolderPath     string         `json:"folder" formam:"folder" validate:"required"`                 // Folder path to store captures
	FirstFlags     uint           `json:"-"`                                                          // bit set for First booleans
	LastFlags      uint           `json:"-"`                                                          // bit set for Last booleans
	WebcamTZ       string         `json:"-"`                                                          // timezone of the webcam (e.g., "America/Los_Angeles")
	WebcamLoc      *time.Location `json:"-"`                                                          // time.Locaion of the webcam
	SunriseUTC     time.Time      `json:"-"`                                                          // sunrise at webcam lat/long (UTC)
	SolarNoonUTC   time.Time      `json:"-"`                                                          // solar noon at webcam lat/long (UTC)
	SunsetUTC      time.Time      `json:"-"`                                                          // sunset at webcam lat/long (UTC)
	CaptureTimes   CaptureTimes   `json:"-"`                                                          // Times (in time zone where the code is running) to capture images
	NextCapture    int            `json:"-"`                                                          // index in CaptureTimes[] of next (future) capture time
	Backoff        int64          `json:"-"`                                                          // delay image retrieval attempts when errors encountered
}

// newTLDef initializes a TLDef structure
func newTLDef() *TLDef {
	tld := TLDef{}
	tld.CaptureTimes = []time.Time{} // prefer an empty slice so json.Marshal() will emit "[]"

	return &tld
}

// SetCaptureTimes calculate all capture times for the specified date
// and initializes NextCapture
func (tld *TLDef) SetCaptureTimes(ctx context.Context, date time.Time) error {
	sn := "main.TLDef.SetCaptureTimes"
	var err error

	// log.Printf("%s, %s date: %v, CaptureTimes (len %d): %v\n", sn, tld.Name, date, len(tld.CaptureTimes), tld.CaptureTimes)

	srv.mu.Lock()
	lenCT := len(tld.CaptureTimes)
	if lenCT > 0 {
		if time.Now().Before(tld.CaptureTimes[lenCT-1]) {
			srv.mu.Unlock()
			return fmt.Errorf("%s called before all CaptureTimes have passed", sn)
		}
		tld.CaptureTimes = []time.Time{} // all existing TLDef times have passed, start with an empty slice (preferred so json.Marshal() will emit "[]")
	}
	srv.mu.Unlock()

	if err = tld.SetWebcamTZ(ctx); err != nil { // establish timezone of webcam
		log.Printf("%s, %s: %v\n", sn, tld.Name, err)
		return err
	}

	if err = tld.GetSolarTimes(ctx, date); err != nil { // set sunrise, solar noon, and sunset for specified date
		log.Printf("%s, %s: %v\n", sn, tld.Name, err)
		return err
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()

	if err := tld.SetFirstCapture(); err != nil {
		log.Printf("%s, %s: %v\n", sn, tld.Name, err)
		return err
	}

	if err := tld.SetAdditional(); err != nil { // also sets Last capture time
		log.Printf("%s, %s: %v\n", sn, tld.Name, err)
		return err
	}

	sort.Sort(tld.CaptureTimes)

	// log.Printf("%s, %s CaptureTimes (len %d): %+v\n",
	// 	sn, tld.Name, len(tld.CaptureTimes), tld.CaptureTimes)
	return nil
}

// SetFirstCapture adds FirstTime or FirstSunrise to CaptureTimes
func (tld *TLDef) SetFirstCapture() error {
	sn := "SetFirstCapture"

	if bits.OnesCount(tld.FirstFlags) == 0 || bits.OnesCount(tld.FirstFlags) > 1 {
		return fmt.Errorf("%s, must specify one of Sunrise, Sunrise +30, or Sunrise +60; or First Time", sn)
	}

	var mins30 time.Duration
	mins30, _ = time.ParseDuration("30m")
	var mins60 time.Duration
	mins60, _ = time.ParseDuration("60m")

	switch {
	case (firstSunrise & tld.FirstFlags) != 0: // add local time of sunrise (where this code is running)
		tld.CaptureTimes = append(tld.CaptureTimes, tld.SunriseUTC.In(srv.localLoc))
	case (firstSunrise30 & tld.FirstFlags) != 0: // add local time of sunrise + 30 minutes
		tld.CaptureTimes = append(tld.CaptureTimes, tld.SunriseUTC.In(srv.localLoc).Add(mins30))
	case (firstSunrise60 & tld.FirstFlags) != 0: // add local time of sunrise + 60 minutes
		tld.CaptureTimes = append(tld.CaptureTimes, tld.SunriseUTC.In(srv.localLoc).Add(mins60))
	}

	// log.Printf("%s, %s CaptureTimes (len %d): %+v\n",
	// 	sn, tld.Name, len(tld.CaptureTimes), tld.CaptureTimes)
	return nil
}

// SetFirstLastFlags sets the FirstFlags and LastFlags bitmaps based on
// the TLDef values First* and Last*, for easier error checking
func (tld *TLDef) SetFirstLastFlags() error {
	sn := "SetFirstLastFlags"

	tld.FirstFlags = 0
	if tld.FirstTime {
		tld.FirstFlags = tld.FirstFlags | firstTime
		// log.Printf("%s, %s: FirstTime found, FirstFlags %b\n", sn, tld.Name, tld.FirstFlags)
	}
	if tld.FirstSunrise {
		tld.FirstFlags = tld.FirstFlags | firstSunrise
		// log.Printf("%s, %s: FirstSunrise found, FirstFlags %b\n", sn, tld.Name, tld.FirstFlags)
	}
	if tld.FirstSunrise30 {
		tld.FirstFlags = tld.FirstFlags | firstSunrise30
		// log.Printf("%s, %s: FirstSunrise30 found, FirstFlags %b\n", sn, tld.Name, tld.FirstFlags)
	}
	if tld.FirstSunrise60 {
		tld.FirstFlags = tld.FirstFlags | firstSunrise60
		// log.Printf("%s, %s: FirstSunrise60 found, FirstFlags %b\n", sn, tld.Name, tld.FirstFlags)
	}
	if bits.OnesCount(tld.FirstFlags) == 0 || bits.OnesCount(tld.FirstFlags) > 1 {
		return fmt.Errorf("%s, must specify one of Sunrise, Sunrise +30, or Sunrise +60; or First Time", sn)
	}

	tld.LastFlags = 0
	if tld.LastTime {
		tld.LastFlags = tld.LastFlags | lastTime
		// log.Printf("%s, %s: LastTime found, LastFlags %b\n", sn, tld.Name, tld.LastFlags)
	}
	if tld.LastSunset {
		tld.LastFlags = tld.LastFlags | lastSunset
		// log.Printf("%s, %s: LastSunset found, LastFlags %b\n", sn, tld.Name, tld.LastFlags)
	}
	if tld.LastSunset30 {
		tld.LastFlags = tld.LastFlags | lastSunset30
		// log.Printf("%s, %s: LastSunset30 found, LastFlags %b\n", sn, tld.Name, tld.LastFlags)
	}
	if tld.LastSunset60 {
		tld.LastFlags = tld.LastFlags | lastSunset60
		// log.Printf("%s, %s: LastSunset60 found, LastFlags %b\n", sn, tld.Name, tld.LastFlags)
	}
	if bits.OnesCount(tld.LastFlags) == 0 || bits.OnesCount(tld.LastFlags) > 1 {
		return fmt.Errorf("%s, must specify one of Sunset, Sunset -30, or Sunset -60; or Last Time", sn)
	}

	// log.Printf("%s, exit SetFirstLastFlags for TLDef (%p), tld.FirstFlags %b, tld.LastFlags %b\n",
	// 	sn, tld, tld.FirstFlags, tld.LastFlags)
	return nil
}

// SetAdditional adds the the Last capture time, and the specified number of
// additional capture times to CaptureTimes
func (tld *TLDef) SetAdditional() error {
	sn := "SetAdditional"

	if err := tld.SetLastCapture(); err != nil { // establish the last capture time, with error checking
		log.Printf("%s, %s: %v\n", sn, tld.Name, err)
		return err
	}

	// both First and Last captures now in CaptureTimes
	first := tld.CaptureTimes[0]
	last := tld.CaptureTimes[1]

	tld.CaptureTimes = *new([]time.Time) // create a new slice with just the first capture time
	tld.CaptureTimes = append(tld.CaptureTimes, first)

	switch {
	case tld.Additional == 0:
		// do nothing

	case tld.Additional == 1:
		// TODO: handle when LastTime capture occurs before solar noon
		// add local time corresponding to solar noon as the additional capture time
		tld.CaptureTimes = append(tld.CaptureTimes, tld.SolarNoonUTC.In(srv.localLoc))

	case tld.Additional%2 == 0:
		tld.SplitTime(first, last, tld.Additional)

	case tld.Additional%2 == 1:
		n := (tld.Additional - 1) / 2                                                  // one of the added capture times will be solar noon
		tld.SplitTime(first, tld.SolarNoonUTC.In(srv.localLoc), n)                     // add the first half the additional capture times
		tld.CaptureTimes = append(tld.CaptureTimes, tld.SolarNoonUTC.In(srv.localLoc)) // add solar noon
		tld.SplitTime(tld.SolarNoonUTC.In(srv.localLoc), last, n)                      // add the second half
	}

	tld.CaptureTimes = append(tld.CaptureTimes, last) // add the last capture time to the new slice

	// log.Printf("%s, %s IsSorted %t\n", sn, tld.Name, sort.IsSorted(tld.CaptureTimes))

	// log.Printf("%s, %s SetAdditional %d, CaptureTimes (len %d): %+v\n",
	// 	sn, tld.Name, tld.Additional, len(tld.CaptureTimes), tld.CaptureTimes)
	return nil
}

// implement sort.Interface on CaptureTime
func (ct CaptureTimes) Len() int {
	return len(ct)
}
func (ct CaptureTimes) Less(i, j int) bool {
	return ct[i].Before(ct[j])
}
func (ct CaptureTimes) Swap(i, j int) {
	// sn := "CaptureTimes.Swap"
	// log.Printf("%s, swapping %v and %v\n", sn, ct[i], ct[j])
	ct[i], ct[j] = ct[j], ct[i]
}

// SplitTime adds N capture times between the provided times
func (tld *TLDef) SplitTime(first time.Time, last time.Time, n int) {
	diff := last.Unix() - first.Unix()
	interval := diff / (int64)(n+1)

	base := first
	for i := 0; i < n; i++ {
		toAdd := (time.Duration)(interval) * time.Second
		next := base.Add(toAdd)
		next = TimeToSecond(next)
		tld.CaptureTimes = append(tld.CaptureTimes, next)
		base = next
	}
}

// SetLastCapture adds LastTime or LastSunset to CaptureTimes
func (tld *TLDef) SetLastCapture() error {
	sn := "SetLastCapture"

	if bits.OnesCount(tld.LastFlags) == 0 || bits.OnesCount(tld.LastFlags) > 1 {
		return fmt.Errorf("%s, must specify one of Sunset, Sunset -30, or Sunset -60; or Last Time", sn)
	}

	var mins30 time.Duration
	mins30, _ = time.ParseDuration("30m")
	var mins60 time.Duration
	mins60, _ = time.ParseDuration("60m")

	switch {
	case (lastSunset & tld.LastFlags) != 0: // add local time of sunset (where this code is running)
		tld.CaptureTimes = append(tld.CaptureTimes, tld.SunsetUTC.In(srv.localLoc))
	case (lastSunset30 & tld.LastFlags) != 0: // "add" -30 minutes to local time of sunset
		tld.CaptureTimes = append(tld.CaptureTimes, tld.SunsetUTC.In(srv.localLoc).Add(-mins30))
	case (lastSunset60 & tld.LastFlags) != 0: // "add" -60 minutes to local time of sunset
		tld.CaptureTimes = append(tld.CaptureTimes, tld.SunsetUTC.In(srv.localLoc).Add(-mins60))
	}

	// log.Printf("%s, %s CaptureTimes (len %d): %+v\n",
	// 	sn, tld.Name, len(tld.CaptureTimes), tld.CaptureTimes)
	return nil
}

// UpdateNextCapture adjusts NextCapture to reference the element with the
// next CaptureTime (first element with time > baseTime), or if none are left
// (today's captures have all been performed), updates CaptureTimes with
// tomorrow's capture times
func (tld *TLDef) UpdateNextCapture(ctx context.Context, baseTime time.Time) error {
	sn := "UpdateNextCapture"

	srv.mu.Lock()
	if !sort.IsSorted(tld.CaptureTimes) {
		sort.Sort(tld.CaptureTimes)
	}
	tld.NextCapture = 0
	now := baseTime.In(srv.localLoc)
	for _, t := range tld.CaptureTimes {
		if t.After(now) {
			break
		}
		tld.NextCapture++
	}
	needsNewDay := tld.NextCapture >= len(tld.CaptureTimes)
	tomorrow := baseTime.AddDate(0, 0, 1)
	srv.mu.Unlock()

	if needsNewDay {
		retryDelays := [...]time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second}
		var err error
		for i := 0; ; i++ {
			if err = tld.SetCaptureTimes(ctx, tomorrow); err == nil {
				break
			}
			if i >= len(retryDelays) {
				log.Printf("%s, %s SetCaptureTimes failed after %d attempts: %v\n",
					sn, tld.Name, len(retryDelays)+1, err)
				return err
			}
			log.Printf("%s, %s SetCaptureTimes failed (attempt %d/%d): %v, retrying in %v\n",
				sn, tld.Name, i+1, len(retryDelays)+1, err, retryDelays[i])
			select {
			case <-time.After(retryDelays[i]):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		srv.mu.Lock()
		tld.NextCapture = 0
		log.Printf("%s, %s CaptureTimes set for tomorrow; NextCapture: %d, CaptureTimes (len %d): %v\n",
			sn, tld.Name, tld.NextCapture, len(tld.CaptureTimes), tld.CaptureTimes)
		srv.mu.Unlock()
	}

	return nil
}

// NextCaptureTime returns the time of the next capture
func (tld TLDef) NextCaptureTime() time.Time {
	next := tld.CaptureTimes[tld.NextCapture]
	return next
}

// const Margin = 100 * time.Millisecond

// IsTimeForCapture determines if it's time to capture an image
func (tld TLDef) IsTimeForCapture() bool {
	b := time.Now().After(tld.NextCaptureTime())
	return b
}

// TimeToSecond returns the provided time rounded down to an even second
func TimeToSecond(t time.Time) time.Time {
	// sn := "timelapse.TimeToSecond"

	toSecond := t.Truncate(time.Second)
	// layout := "Jan 2 2006 15:04:05 -0700 MST"
	// log.Printf("%s, parameter: %s, return value: %s\n", sn, t.Format(time.RFC3339Nano), toSecond.Format(layout))
	return toSecond
}

// ********** ********** ********** ********** ********** **********

type masterTLDefs []*TLDef

// newMasterTLDefs returns a new (empty) master timelapse definition object
func newMasterTLDefs() *masterTLDefs {
	var mtld = new(masterTLDefs)

	// log.Printf("newMasterTLDefs, %p, %+v", mtld, mtld)
	return mtld
}

// Read reads the master timelapse definitions file into the masterTLDefs
// slice of its receiver
func (mtld masterTLDefs) Read(path string) error {
	sn := "mtld.Read"

	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("%s, os.ReadFile: %v\n", sn, err)
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("master file empty")
	}
	// log.Printf("mtld.Read, contents of %s: %q\n", masterFile, data)

	err = json.Unmarshal(data, srv.mtld)
	if err != nil {
		log.Printf("%s, json.Unmarshal: %v\n", sn, err)
		return err
	}

	// validate the TLDefs we just read
	mtldSlice := *srv.mtld
	for i, tld := range mtldSlice { // validate the TLDef structs within mtld
		if err := srv.validate.Struct(tld); err != nil {
			log.Printf("%s, validate.Struct, element %d (%s): %v\n", sn, i, tld.Name, err)
			return err
		}
		// log.Printf("%s, validated mtld element %d: (%p) %+v\n", sn, i, &tld, tld)

		if err := tld.SetFirstLastFlags(); err != nil {
			log.Printf("%s, %s: SetFirstLastFlags: %v\n", sn, tld.Name, err)
			return err
		}
		// log.Printf("%s, after SetFirstLastFlags, mtld element %d: (%p) %+v\n", sn, i, &tld, tld)
	}

	return nil
}

// Write writes the masterTLDefs to the master timelapse definitions file
func (mtld masterTLDefs) Write() error {
	sn := "mtld.Write"

	var buf []byte
	var err error

	// validate the TLDefs in mtld before writing them
	mtldSlice := mtld
	for i, tld := range mtldSlice { // validate the TLDef structs within mtld
		if err := srv.validate.Struct(tld); err != nil {
			log.Printf("%s, validate.Struct, element %d (%s): %v\n", sn, i, tld.Name, err)
			return err
		}
	}

	if buf, err = json.Marshal(mtld); err != nil {
		log.Printf("%s, json.Marshal: %v\n", sn, err)
		return err
	}

	filename := filepath.Join(srv.config.path, masterFile)
	if err = os.WriteFile(filename, buf, 0644); err != nil { // -rw-r--r--
		log.Printf("%s, os.WriteFile: %v\n", sn, err)
		return err
	}

	log.Printf("mtld.Write, %s", filename)
	return nil
}

// Append appends a timelapse definition to the masterTLDefs slice
func (mtld *masterTLDefs) Append(newTLD *TLDef) error {
	*mtld = append(*mtld, newTLD)

	// log.Printf("mtld.Add, %+v", *mtld)
	return nil
}

// Delete timelapse definition(s) with Name matching prefix from
// the masterTLDefs slice. Primarily used to cleanup after testing.
func (mtld *masterTLDefs) Delete(prefix string) *masterTLDefs {
	sn := "masterTLDefs.Delete"

	var newMTLD masterTLDefs
	for _, ptld := range *mtld {
		if !strings.HasPrefix(ptld.Name, prefix) {
			log.Printf("%s, retain %s\n", sn, (*ptld).Name)
			newMTLD = append(newMTLD, ptld)
		}
	}
	return &newMTLD
}

// ********** ********** ********** ********** ********** **********

// SSDayInfo holds response fields from sunrise-sunset.org
type SSDayInfo struct { // all times are UTC
	linkTLDef                 *TLDef // link to associated TLDef
	Date                      time.Time
	Latitude                  float64 `json:"-" validate:"latitude,required"`  // Latitude of webcam
	Longitude                 float64 `json:"-" validate:"longitude,required"` // Longitude of webcam
	SSDISunrise               string  `json:"sunrise"`
	SSDISunset                string  `json:"sunset"`
	SSDISolarNoon             string  `json:"solar_noon"`
	DayLength                 int     `json:"day_length"`
	CivilTwilightBegin        string  `json:"civil_twilight_begin"`
	CivilTwilightEnd          string  `json:"civil_twilight_end"`
	NauticalTwilightBegin     string  `json:"nautical_twilight_begin"`
	NauticalTwilightEnd       string  `json:"nautical_twilight_end"`
	AstronomicalTwilightBegin string  `json:"astronomical_twilight_begin"`
	AstronomicalTwilightEnd   string  `json:"astronomical_twilight_end"`
}

// GetSolarTimes uses the specified date and the TLDef's latitude/longitude
// to establish sunrise, solar noon, and sunset times (UTC) and store
// them in the TLDef
func (tld *TLDef) GetSolarTimes(ctx context.Context, date time.Time) error {
	sn := "main.tld.GetSolarTimes"
	// log.Printf("%s, %s date: %v\n", sn, tld.Name, date)

	ssdi := NewSSDayInfo(tld)
	ssdi.Date = date

	query := ssdi.buildQuery()
	req, err := http.NewRequestWithContext(ctx, "GET", query, nil)
	if err != nil {
		log.Printf("%s, %s http.NewRequest: %v\n", sn, tld.Name, err)
		return err
	}

	// log.Printf("%s, %s %s %s\n", sn, tld.Name, method, query)
	resp, err := apiHTTPClient.Do(req)
	if err != nil {
		log.Printf("%s, %s http.Client.Do: %v", sn, tld.Name, err)
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("%s, %s io.ReadAll: %v", sn, tld.Name, err)
		return err
	}
	// log.Printf("%s, %s resp.Body: %s", sn, tld.Name, body)

	// change each time's "+00:00" suffix to "Z" to clean up time.Parse result
	body = bytes.ReplaceAll(body, []byte(`+00:00"`), []byte(`Z"`))

	var wrapper struct {
		Results SSDayInfo `json:"results"`
		Status  string    `json:"status"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		log.Printf("%s, %s json.Unmarshal: %v", sn, tld.Name, err)
		return err
	}

	if wrapper.Status != "OK" {
		return fmt.Errorf("%s, %s sunrise-sunset.org returned status: %s", sn, tld.Name, wrapper.Status)
	}

	if tld.SunriseUTC, err = time.Parse(timeLayout, wrapper.Results.SSDISunrise); err != nil {
		log.Printf("%s, %s time.Parse(%s): %v", sn, tld.Name, wrapper.Results.SSDISunrise, err)
		return err
	}

	if tld.SolarNoonUTC, err = time.Parse(timeLayout, wrapper.Results.SSDISolarNoon); err != nil {
		log.Printf("%s, %s time.Parse(%s): %v", sn, tld.Name, wrapper.Results.SSDISolarNoon, err)
		return err
	}

	if tld.SunsetUTC, err = time.Parse(timeLayout, wrapper.Results.SSDISunset); err != nil {
		log.Printf("%s, %s time.Parse(%s): %v", sn, tld.Name, wrapper.Results.SSDISunset, err)
		return err
	}

	// log.Printf("%s, %s SunriseUTC: %v, SolarNoonUTC: %v, SunsetUTC: %v\n", sn, tld.Name, tld.SunriseUTC, tld.SolarNoonUTC, tld.SunsetUTC)
	return nil
}

// NewSSDayInfo creates a new instance of SSDayInfo
func NewSSDayInfo(tld *TLDef) *SSDayInfo {
	ssdi := &SSDayInfo{}
	ssdi.linkTLDef = tld
	ssdi.Latitude = tld.Latitude
	ssdi.Longitude = tld.Longitude

	return ssdi
}

// buildQuery returns the query string for sunrise-sunset.org API requests
func (ssdi SSDayInfo) buildQuery() string {
	// sn := "main.SSDayInfo.buildQuery"

	queryParams := url.Values{}

	queryParams.Add("lat", fmt.Sprintf("%.7f", ssdi.linkTLDef.Latitude))
	queryParams.Add("lng", fmt.Sprintf("%.7f", ssdi.linkTLDef.Longitude))

	year, month, day := ssdi.Date.Date()
	date := fmt.Sprintf("%4d-%02d-%02d", year, month, day)
	queryParams.Add("date", date)

	queryParams.Add("formatted", "0") // ISO 8601, e.g., "2015-05-21T05:05:35+00:00"

	query := "https://api.sunrise-sunset.org/json?"
	query += queryParams.Encode()

	// log.Printf("%s, %s query: %q\n", sn, ssdi.linkTLDef.Name, query)
	return query
}

// TimeZoneDB holds response fields from timezonedb.com
type TimeZoneDB struct {
	linkTLDef        *TLDef // link to associated TLDef
	Status           string `json:"status"`
	Message          string `json:"message"`
	CountryCode      string `json:"countryCode"`
	CountryName      string `json:"countryName"`
	RegionName       string `json:"regionName"`
	CityName         string `json:"cityName"`
	ZoneName         string `json:"zoneName"`
	Abbreviation     string `json:"abbreviation"`
	GmtOffset        int    `json:"gmtOffset"`
	Dst              string `json:"dst"`
	ZoneStart        int    `json:"zoneStart"`
	ZoneEnd          int    `json:"zoneEnd"`
	NextAbbreviation string `json:"nextAbbreviation"`
	Timestamp        int    `json:"timestamp"`
	Formatted        string `json:"formatted"`
}

// NewTimeZoneDB creates a new instance of TimeZoneDB
func NewTimeZoneDB(tld *TLDef) *TimeZoneDB {
	tzdb := &TimeZoneDB{}
	tzdb.linkTLDef = tld
	return tzdb
}

// SetWebcamTZ determines and stores the timezone of the webcam
// based on TLDef's latitude/longitude. It is called daily when
// capture times for the day are set, to accomodate DST changes.
func (tld *TLDef) SetWebcamTZ(ctx context.Context) error {
	sn := "main.tld.SetWebcamTZ"

	if srv.config.tzdbAPI == "" {
		return fmt.Errorf("%s, tzdbAPI not configured: set TIMELAPSE_TZDB environment variable", sn)
	}

	var err error

	tzdb := NewTimeZoneDB(tld)
	query := tzdb.buildQuery(tld)

	var resp *http.Response
	for {
		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, "GET", query, nil)
		if err != nil {
			log.Printf("%s, %s http.NewRequest: %v\n", sn, tld.Name, err)
			return err
		}
		resp, err = apiHTTPClient.Do(req)
		if err != nil {
			log.Printf("%s, %s http.Client.Do: %v", sn, tld.Name, err)
			return err
		}
		if resp.StatusCode == http.StatusOK {
			break
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests { // rate limited to 1 request/second
			sleep := time.Duration(1+rand.Intn(4)) * time.Second
			log.Printf("%s, %s received http.StatusTooMany (429), sleeping %v...\n", sn, tld.Name, sleep)
			time.Sleep(sleep)
		}
	}
	defer resp.Body.Close()

	var body []byte
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("%s, %s io.ReadAll: %v", sn, tld.Name, err)
		return err
	}
	// log.Printf("%s, %s resp.Body: %s", sn, tld.Name, body)

	if err = json.Unmarshal(body, tzdb); err != nil { // unmarshall all provided fields
		log.Printf("%s, %s json.Unmarshal: %v", sn, tld.Name, err)
		return err
	}

	tld.WebcamTZ = tzdb.ZoneName
	if tld.WebcamLoc, err = time.LoadLocation(tld.WebcamTZ); err != nil {
		log.Printf("%s, %s time.LoadLocation(%s): %v", sn, tld.Name, tld.WebcamTZ, err)
		return err
	}

	// log.Printf("%s, %s WebcamLoc: %v\n", sn, tld.Name, tld.WebcamLoc)
	return nil
}

// buildQuery builds the query string for TimeZoneDB.com API requests
func (tzdb *TimeZoneDB) buildQuery(tld *TLDef) string {
	// sn := "main.webcamTZ.buildQuery"

	queryParams := url.Values{}

	queryParams.Add("key", srv.config.tzdbAPI)
	queryParams.Add("format", "json")
	queryParams.Add("by", "position")
	queryParams.Add("lat", fmt.Sprintf("%.7f", tld.Latitude))
	queryParams.Add("lng", fmt.Sprintf("%.7f", tld.Longitude))

	query := "http://api.timezonedb.com/v2.1/get-time-zone?"
	query += queryParams.Encode()

	// log.Printf("%s, %s query: %q\n", sn, tzdb.linkTLDef.Name, query)
	return query
}
