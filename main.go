// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// main.go defines the server struct, startup, shutdown, and logging.
// All external dependencies (HTTP clients, storage, renderer) are injected
// into the server at startup, so tests can substitute fakes.

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // embed IANA timezone database (required on Windows / scratch containers)

	"github.com/go-playground/validator/v10"
	"github.com/julienschmidt/httprouter"
)

// server holds all shared state and injected dependencies.
type server struct {
	router        *httprouter.Router
	validate      *validator.Validate
	config        *Config
	tmplDashboard    *template.Template
	tmplNewWebcam    *template.Template
	tmplLatlong      *template.Template
	tmplLogs         *template.Template
	tmplWriteFailure *template.Template
	webcams      *Webcams       // all configured webcams; protected by mu
	storage      Storage
	renderer     Renderer
	tz           TimezoneClient
	solar        SolarClient
	fetcher      ImageFetcher
	ctx          context.Context
	cancel       context.CancelFunc
	webcamCtx    context.Context    // child of ctx; cancelled to stop capture goroutines
	webcamCancel context.CancelFunc // protected by mu when read in handleNew
	wg           sync.WaitGroup     // maintenance goroutines (newDayMaintenance)
	webcamWg     sync.WaitGroup     // capture goroutines only
	mu           sync.RWMutex       // protects webcams, webcamCtx, webcamCancel
	startTime    time.Time
	trial        TrialState
}

var (
	currentLogFile      *os.File
	currentDebugLogFile *os.File
)

// multiHandler fans a single slog.Record out to multiple slog.Handler instances.
type multiHandler struct{ handlers []slog.Handler }

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, hh := range h.handlers {
		if hh.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, hh := range h.handlers {
		if hh.Enabled(ctx, r.Level) {
			_ = hh.Handle(ctx, r.Clone())
		}
	}
	return nil
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	hs := make([]slog.Handler, len(h.handlers))
	for i, hh := range h.handlers {
		hs[i] = hh.WithAttrs(attrs)
	}
	return &multiHandler{handlers: hs}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	hs := make([]slog.Handler, len(h.handlers))
	for i, hh := range h.handlers {
		hs[i] = hh.WithGroup(name)
	}
	return &multiHandler{handlers: hs}
}

func main() {
	if !ensureSingleInstance() {
		return
	}

	srv := newServer()

	path := filepath.Join(srv.config.Path, masterFile)
	if err := srv.webcams.Read(path, srv.validate); err != nil {
		log.Fatalf("main: read %s: %v", path, err)
	}

	if err := openLogFile(srv.config.LogDir, time.Now()); err != nil {
		log.Printf("main: cannot open log file in %s: %v — log output going to console", srv.config.LogDir, err)
	}

	// Start daily log-rotation goroutine.
	srv.wg.Add(1)
	go newDayMaintenance(srv.ctx, srv)

	// Launch capture goroutines in the background so the UI window can open
	// immediately. The 2 s sleep between launches respects timezonedb.com's
	// 1 req/s rate limit. launchWg ensures all webcamWg.Add calls complete
	// before webcamWg.Wait is called during shutdown.
	var launchWg sync.WaitGroup
	launchWg.Add(1)
	go func() {
		defer launchWg.Done()
		if srv.trial.capturesStopped() {
			log.Printf("main: trial %s — captures disabled", srv.trial)
			return
		}
		for _, wc := range *srv.webcams {
			srv.webcamWg.Add(1)
			go capture(srv.webcamCtx, wc, time.Duration(srv.config.PollSecs)*time.Second, srv)
			time.Sleep(2 * time.Second)
		}
	}()

	if err := srv.initTemplates(); err != nil {
		log.Fatalf("main: initTemplates: %v", err)
	}

	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("main: fs.Sub staticFS: %v", err)
	}
	srv.router.Handler("GET", "/static/*filepath",
		http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	srv.router.GET("/", srv.handleHome())
	srv.router.GET("/new", srv.handleGetNew())
	srv.router.GET("/latlong", srv.handleGetLatlong())
	srv.router.POST("/probe", srv.handleProbe())
	srv.router.GET("/devices", srv.handleDevices())
	srv.router.POST("/new", srv.handleNew())
	srv.router.GET("/info", srv.handleInfo())
	srv.router.GET("/status", srv.handleStatus())
	srv.router.GET("/next", srv.handleNext())
	srv.router.GET("/logs", srv.handleLogs())
	srv.router.POST("/render", srv.handleRender())
	srv.router.POST("/reload", srv.handleReload())

	hs := &http.Server{
		Addr:         "127.0.0.1:" + srv.config.Port,
		Handler:      srv.router,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	log.Printf("main: listening on :%s", srv.config.Port)
	go startListening(hs)
	go printStartupSummary(srv.config.Port)

	runUI(srv.config.Port)
	log.Printf("main: shutting down")
	srv.cancel()
	launchWg.Wait() // ensure all webcamWg.Add calls complete before Wait
	srv.webcamWg.Wait()
	srv.wg.Wait()

	if currentLogFile != nil {
		currentLogFile.Close()
	}
}

// newServer constructs a server with production dependencies.
func newServer() *server {
	cfg := &Config{}
	cfg.Load()

	if cfg.TzdbAPI == "" {
		fmt.Fprintln(os.Stderr, "LOGSCENE_TZDB (or -tzdb flag) is required")
		os.Exit(1)
	}

	installDate, err := readOrSetInstallDate()
	if err != nil {
		log.Fatalf("newServer: cannot read trial data from registry: %v\n"+
			"  LogScene stores its trial state in HKCU\\Software\\LogScene.\n"+
			"  If this error persists, contact support@logscene.net.", err)
	}
	trial := computeTrialState(installDate)
	log.Printf("newServer: trial=%s (installed %s)", trial, installDate.Format("2006-01-02"))

	store := buildStorage(cfg.Storage)

	ctx, cancel := context.WithCancel(context.Background())
	webcamCtx, webcamCancel := context.WithCancel(ctx)
	s := &server{
		router:       httprouter.New(),
		validate:     newValidator(),
		config:       cfg,
		webcams:      newWebcams(),
		storage:      store,
		renderer:     NewLocalRenderer(),
		tz:           NewHTTPTimezoneClient(cfg.TzdbAPI),
		solar:        NewHTTPSolarClient(),
		fetcher:      NewHTTPImageFetcher(),
		ctx:          ctx,
		cancel:       cancel,
		webcamCtx:    webcamCtx,
		webcamCancel: webcamCancel,
		startTime:    time.Now(),
		trial:        trial,
	}
	return s
}

// buildStorage returns the Storage implementation for the configured backend.
func buildStorage(backend string) Storage {
	switch strings.ToLower(backend) {
	case "gcs":
		log.Fatal("GCS storage not yet implemented — set LOGSCENE_STORAGE=local")
	case "s3":
		log.Fatal("S3 storage not yet implemented — set LOGSCENE_STORAGE=local")
	}
	return NewLocalStorage() // default: local
}

// startListening calls ListenAndServe and logs fatal errors.
func startListening(hs *http.Server) {
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		port := hs.Addr
		if i := strings.LastIndex(hs.Addr, ":"); i >= 0 {
			port = hs.Addr[i+1:]
		}
		log.Fatalf("startListening: cannot listen on port %s: %v", port, err)
	}
}

// printStartupSummary waits for capture goroutines to initialise, then calls
// /status and /next and prints the responses to stdout so the operator gets
// confirmation in the terminal even though log output has been redirected.
func printStartupSummary(port string) {
	time.Sleep(3 * time.Second)
	client := &http.Client{Timeout: 5 * time.Second}
	base := "http://localhost:" + port
	for _, path := range []string{"/info", "/status", "/next"} {
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

// newDayMaintenance wakes at server-local midnight each day to rotate the log file.
func newDayMaintenance(ctx context.Context, srv *server) {
	defer srv.wg.Done()
	for {
		now := time.Now()
		nextMidnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 1, 0, now.Location())
		select {
		case <-time.After(time.Until(nextMidnight)):
			if err := openLogFile(srv.config.LogDir, time.Now()); err != nil {
				log.Printf("newDayMaintenance: openLogFile: %v", err)
			}
		case <-ctx.Done():
			log.Printf("newDayMaintenance: context cancelled — exiting")
			return
		}
	}
}

// openLogFile opens (or creates) a dated log file, redirects the standard
// logger to it, closes the previous log file, and reconfigures slog with a
// TextHandler (user-facing, Info+) and a JSONHandler (debug, Debug+).
func openLogFile(logDir string, date time.Time) error {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("openLogFile: MkdirAll %s: %w", logDir, err)
	}

	name := filepath.Join(logDir, "logscene-"+date.Format("2006-01-02")+".log")
	f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("openLogFile: %w", err)
	}
	log.SetOutput(f)
	if currentLogFile != nil {
		currentLogFile.Close()
	}
	currentLogFile = f

	// Open debug log (non-fatal: support bundle is best-effort).
	debugName := filepath.Join(logDir, "logscene-debug-"+date.Format("2006-01-02")+".log")
	df, debugErr := os.OpenFile(debugName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if debugErr != nil {
		log.Printf("openLogFile: cannot open debug log %s: %v", debugName, debugErr)
	} else {
		if currentDebugLogFile != nil {
			currentDebugLogFile.Close()
		}
		currentDebugLogFile = df
	}

	// Wire slog: Info+ to user log; Debug+ to debug log (if open).
	userHandler := slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})
	var h slog.Handler = userHandler
	if currentDebugLogFile != nil {
		debugHandler := slog.NewJSONHandler(currentDebugLogFile, &slog.HandlerOptions{Level: slog.LevelDebug})
		h = &multiHandler{handlers: []slog.Handler{userHandler, debugHandler}}
	}
	slog.SetDefault(slog.New(h))

	log.Printf("openLogFile: logging to %s", name)
	return nil
}
