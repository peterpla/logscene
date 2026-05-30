package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var testCleanupOnce sync.Once

func cleanupTestEntries() {
	testCleanupOnce.Do(func() {
		newMTLD := srv.mtld.Delete("test")
		srv.mtld = newMTLD
		if err := srv.mtld.Write(); err != nil {
			log.Printf("cleanupTestEntries: %v", err)
		}
	})
}

func newBaseTLD() TLDef {
	return TLDef{
		Name:         "Kohm Yah-man-yeh",
		URL:          "https://www.nps.gov/webcams-lavo/kyvc_webcam1.jpg?1589316288166",
		Latitude:     40.437787,
		Longitude:    -121.5360307,
		FirstTime:    false,
		FirstSunrise: true,
		LastTime:     false,
		LastSunset:   true,
		FirstFlags:   firstSunrise,
		LastFlags:    lastSunset,
		Additional:   1,
		FolderPath:   "C:\\Users\\peter\\OneDrive\\Pictures\\Timelapse\\zzTest", // TODO: assemble path to avoid platform-specific path separator issues
		CaptureTimes: CaptureTimes{sunrise, solarNoon, sunset},
		NextCapture:  0,
	}
}

var loc = time.Local
var sunrise = time.Date(2020, 5, 27, 5, 39, 41, 0, loc)   // Sunrise
var solarNoon = time.Date(2020, 5, 27, 13, 3, 28, 0, loc) // SolarNoon
var sunset = time.Date(2020, 5, 27, 20, 27, 15, 0, loc)   // Sunset

var mins30 time.Duration
var mins60 time.Duration

func TestMain(m *testing.M) {
	var err error
	sn := "TestMain"

	// funcframework.RegisterHTTPFunction("/", capture.WebcamImage)

	srv = newServer()
	srv.ctx, srv.cancel = context.WithCancel(context.Background())

	srv.initTemplates("./templates", ".html") //
	srv.router.ServeFiles("/static/*filepath", http.Dir("static"))
	srv.router.POST("/new", srv.handleNew())
	srv.router.GET("/", srv.handleHome())

	if err = srv.mtld.Read(filepath.Join(srv.config.path, masterFile)); err != nil {
		msg := fmt.Sprintf("%s, srv.mtld.Read: %v", sn, err)
		panic(msg)
	}

	// Use port from configuration, or PORT environment variable
	port := srv.config.port
	if port == "" {
		port = os.Getenv("PORT")
	}

	mins30, _ = time.ParseDuration("30m")
	mins60, _ = time.ParseDuration("60m")

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		log.Printf("%s: interrupted, cleaning up test entries...", sn)
		cleanupTestEntries()
		os.Exit(1)
	}()

	exitcode := m.Run()

	cleanupTestEntries()
	os.Exit(exitcode)
}

func TestTLDef_CaptureImage(t *testing.T) {
	t.Skip()
	// tests := []struct {
	// 	name string
	// 	tld  TLDef
	// 	want time.Time
	// }{
	// 	// TODO: Add test cases.
	// }
	// for _, tt := range tests {
	// 	t.Run(tt.name, func(t *testing.T) {
	// 		if got := tt.tld.CaptureImage(); got != tt.want {
	// 			t.Errorf("TLDef.CaptureImage() got %v, want %v", got, tt.want)
	// 		}
	// 	})
	// }
}

func TestTLDef_TargetFileName(t *testing.T) {
	baseTLD := newBaseTLD()

	otherTLD := newBaseTLD()
	otherTLD.CaptureTimes = CaptureTimes{solarNoon, sunset}
	otherTLD.NextCapture = 1

	tests := []struct {
		name string
		tld  *TLDef
		want string
	}{
		{name: "sunrise",
			tld:  &baseTLD,
			want: baseTLD.FolderPath + "\\" + baseTLD.Name + " " + "20200527053941", // TODO: assemble path to avoid platform-specific path separator issues
		},
		{name: "sunset",
			tld:  &otherTLD,
			want: otherTLD.FolderPath + "\\" + otherTLD.Name + " " + "20200527202715", // TODO: assemble path to avoid platform-specific path separator issues
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tld.TargetFileName(); got != tt.want {
				t.Errorf("TLDef.TargetFileName() got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTLDef_RetrieveImage(t *testing.T) {
	t.Skip()
	// tests := []struct {
	// 	name string
	// 	tld  TLDef
	// 	want time.Time
	// }{
	// 	// TODO: Add test cases.
	// }
	// for _, tt := range tests {
	// 	t.Run(tt.name, func(t *testing.T) {
	// 		respBody, err := tt.tld.RetrieveImage()
	// 		if got != tt.want {
	// 			t.Errorf("TLDef.RetrieveImage() got %v, want %v", got, tt.want)
	// 		}
	// 	})
	// }
}

func Test_server_handleHome(t *testing.T) {
	tests := []struct {
		name       string
		params     []byte
		wantStatus int
		substring  []byte
	}{
		{name: "home",
			params:     []byte(""),
			wantStatus: http.StatusOK,
			substring:  []byte("<title>Timelapse</title>"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest("GET", "/", bytes.NewReader(tt.params))
			if err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			srv.router.ServeHTTP(rr, req)

			if status := rr.Code; status != tt.wantStatus {
				t.Errorf("%s, got %d, want %d", tt.name, rr.Code, tt.wantStatus)
			} else {
				if len(tt.substring) > 0 {
					got := rr.Body.String()
					want := (string)(tt.substring)
					if !strings.Contains(got, want) {
						t.Errorf("%s want substring %q, not found in %q", tt.name, want, got)
					}
				}
			}
		})
	}
}

func Test_server_handleNew(t *testing.T) {
	tests := []struct {
		name       string
		params     map[string]string
		wantStatus int
		substring  []byte
	}{
		{name: "min valid",
			params: map[string]string{
				"name":         "test1",
				"webcamUrl":    "https://www.nps.gov/webcams-lavo/kyvc_webcam1.jpg?1589316288166",
				"latitude":     "40.437787",
				"longitude":    "-121.5360307",
				"firstSunrise": "",
				"lastSunset":   "",
				"additional":   "0",
				"folder":       "C:\\Users\\peter\\OneDrive\\Pictures\\Timelapse\\zzTest", // TODO: assemble path to avoid platform-specific path separator issues
			},
			wantStatus: http.StatusSeeOther,
			substring:  []byte(""),
		},
		{name: "missing name",
			params: map[string]string{
				// "name":         "test1",
				"webcamUrl":    "https://www.nps.gov/webcams-lavo/kyvc_webcam1.jpg?1589316288166",
				"latitude":     "40.437787",
				"longitude":    "-121.5360307",
				"firstSunrise": "",
				"lastSunset":   "",
				"additional":   "0",
				"folder":       "C:\\Users\\peter\\OneDrive\\Pictures\\Timelapse\\zzTest", // TODO: assemble path to avoid platform-specific path separator issues
			},
			wantStatus: http.StatusBadRequest,
			substring:  []byte(""),
		},
		{name: "missing webcamUrl",
			params: map[string]string{
				"name": "test1",
				// "webcamUrl":    "https://www.nps.gov/webcams-lavo/kyvc_webcam1.jpg?1589316288166",
				"latitude":     "40.437787",
				"longitude":    "-121.5360307",
				"firstSunrise": "",
				"lastSunset":   "",
				"additional":   "0",
				"folder":       "C:\\Users\\peter\\OneDrive\\Pictures\\Timelapse\\zzTest", // TODO: assemble path to avoid platform-specific path separator issues
			},
			wantStatus: http.StatusBadRequest,
			substring:  []byte(""),
		},
		{name: "missing lat",
			params: map[string]string{
				"name":      "test1",
				"webcamUrl": "https://www.nps.gov/webcams-lavo/kyvc_webcam1.jpg?1589316288166",
				// "latitude":     "40.437787",
				"longitude":    "-121.5360307",
				"firstSunrise": "",
				"lastSunset":   "",
				"additional":   "0",
				"folder":       "C:\\Users\\peter\\OneDrive\\Pictures\\Timelapse\\zzTest", // TODO: assemble path to avoid platform-specific path separator issues
			},
			wantStatus: http.StatusBadRequest,
			substring:  []byte(""),
		},
		{name: "missing long",
			params: map[string]string{
				"name":      "test1",
				"webcamUrl": "https://www.nps.gov/webcams-lavo/kyvc_webcam1.jpg?1589316288166",
				"latitude":  "40.437787",
				// "longitude":    "-121.5360307",
				"firstSunrise": "",
				"lastSunset":   "",
				"additional":   "0",
				"folder":       "C:\\Users\\peter\\OneDrive\\Pictures\\Timelapse\\zzTest", // TODO: assemble path to avoid platform-specific path separator issues
			},
			wantStatus: http.StatusBadRequest,
			substring:  []byte(""),
		},
		{name: "missing additional",
			params: map[string]string{
				"name":         "test1",
				"webcamUrl":    "https://www.nps.gov/webcams-lavo/kyvc_webcam1.jpg?1589316288166",
				"latitude":     "40.437787",
				"longitude":    "-121.5360307",
				"firstSunrise": "",
				"lastSunset":   "",
				// "additional":   "0",
				"folder": "C:\\Users\\peter\\OneDrive\\Pictures\\Timelapse\\zzTest", // TODO: assemble path to avoid platform-specific path separator issues
			},
			wantStatus: http.StatusBadRequest,
			substring:  []byte(""),
		},
		{name: "additional too small",
			params: map[string]string{
				"name":         "test1",
				"webcamUrl":    "https://www.nps.gov/webcams-lavo/kyvc_webcam1.jpg?1589316288166",
				"latitude":     "40.437787",
				"longitude":    "-121.5360307",
				"firstSunrise": "",
				"lastSunset":   "",
				"additional":   "-1",
				"folder":       "C:\\Users\\peter\\OneDrive\\Pictures\\Timelapse\\zzTest", // TODO: assemble path to avoid platform-specific path separator issues
			},
			wantStatus: http.StatusBadRequest,
			substring:  []byte(""),
		},
		{name: "additional too big",
			params: map[string]string{
				"name":         "test1",
				"webcamUrl":    "https://www.nps.gov/webcams-lavo/kyvc_webcam1.jpg?1589316288166",
				"latitude":     "40.437787",
				"longitude":    "-121.5360307",
				"firstSunrise": "",
				"lastSunset":   "",
				"additional":   "17",
				"folder":       "C:\\Users\\peter\\OneDrive\\Pictures\\Timelapse\\zzTest", // TODO: assemble path to avoid platform-specific path separator issues
			},
			wantStatus: http.StatusBadRequest,
			substring:  []byte(""),
		},
		{name: "missing folder",
			params: map[string]string{
				"name":         "test1",
				"webcamUrl":    "https://www.nps.gov/webcams-lavo/kyvc_webcam1.jpg?1589316288166",
				"latitude":     "40.437787",
				"longitude":    "-121.5360307",
				"firstSunrise": "",
				"lastSunset":   "",
				"additional":   "0",
				// "folder":       "C:\\Users\\peter\\OneDrive\\Pictures\\Timelapse\\zzTest",
			},
			wantStatus: http.StatusBadRequest,
			substring:  []byte(""),
		},
		{name: "sunrise30, sunset30",
			params: map[string]string{
				"name":           "test1",
				"webcamUrl":      "https://www.nps.gov/webcams-lavo/kyvc_webcam1.jpg?1589316288166",
				"latitude":       "40.437787",
				"longitude":      "-121.5360307",
				"firstSunrise30": "",
				"lastSunset30":   "",
				"additional":     "0",
				"folder":         "C:\\Users\\peter\\OneDrive\\Pictures\\Timelapse\\zzTest", // TODO: assemble path to avoid platform-specific path separator issues
			},
			wantStatus: http.StatusSeeOther,
			substring:  []byte(""),
		},
		{name: "sunrise60, sunset60",
			params: map[string]string{
				"name":           "test1",
				"webcamUrl":      "https://www.nps.gov/webcams-lavo/kyvc_webcam1.jpg?1589316288166",
				"latitude":       "40.437787",
				"longitude":      "-121.5360307",
				"firstSunrise60": "",
				"lastSunset60":   "",
				"additional":     "0",
				"folder":         "C:\\Users\\peter\\OneDrive\\Pictures\\Timelapse\\zzTest", // TODO: assemble path to avoid platform-specific path separator issues
			},
			wantStatus: http.StatusSeeOther,
			substring:  []byte(""),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqParams := url.Values{}
			for k, v := range tt.params {
				reqParams.Add(k, v)
			}
			body := reqParams.Encode()
			req, err := http.NewRequest("POST", "/new", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

			rr := httptest.NewRecorder()
			srv.router.ServeHTTP(rr, req)

			if status := rr.Code; status != tt.wantStatus {
				t.Errorf("%s, got %d, want %d", tt.name, rr.Code, tt.wantStatus)
			} else {
				if len(tt.substring) > 0 {
					got := rr.Body.String()
					want := (string)(tt.substring)
					if !strings.Contains(got, want) {
						t.Errorf("%s want substring %q, not found in %q", tt.name, want, got)
					}
				}
			}
		})
	}
}

func Test_server_initTemplates(t *testing.T) {
	t.Skip()
	type args struct {
		dir string
		ext string
	}
	tests := []struct {
		name string
		s    *server
		args args
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.s.initTemplates(tt.args.dir, tt.args.ext)
			// if got := tt.s.initTemplates(); !reflect.DeepEqual(got, tt.want) {
			// 	t.Errorf("server.initTemplates() got %v, want %v", got, tt.want)
			// }
		})
	}
}

func TestConfig_Load(t *testing.T) {
	t.Skip()
	tests := []struct {
		name string
		c    Config
		want Config
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ttc := &tt.c
			ttc.Load()
			if !reflect.DeepEqual(tt.c, tt.want) {
				t.Errorf("Config.Load() got %v, want %v", tt.c, tt.want)
			}
		})
	}
}

func TestNewTLDef(t *testing.T) {
	t.Skip()
	tests := []struct {
		name string
		want TLDef
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newTLDef()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Config.Load() got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTLDef_SetCaptureTimes(t *testing.T) {
	t.Skip("calls real TimeZoneDB API")
	// layout := "Jan 2 2006 15:04:05 -0700 MST"
	loc := time.Local

	day1 := time.Date(2020, 5, 27, 0, 0, 0, 0, loc)
	day1Capture := CaptureTimes{
		time.Date(2020, 5, 27, 5, 39, 41, 0, loc),  // Sunrise
		time.Date(2020, 5, 27, 13, 3, 28, 0, loc),  // SolarNoon
		time.Date(2020, 5, 27, 20, 27, 15, 0, loc), // Sunset
	}

	day2 := time.Date(2020, 5, 28, 0, 0, 0, 0, loc)
	day2Capture := CaptureTimes{
		time.Date(2020, 5, 28, 5, 39, 9, 0, loc),  // Sunrise
		time.Date(2020, 5, 28, 13, 3, 36, 0, loc), // SolarNoon
		time.Date(2020, 5, 28, 20, 28, 2, 0, loc), // Sunset
	}

	tests := []struct {
		name    string
		tld     *TLDef
		day     time.Time
		wantErr bool
		want    CaptureTimes
	}{
		{name: "May27",
			tld: &TLDef{
				Name:         "Kohm Yah-man-yeh",
				URL:          "https://www.nps.gov/webcams-lavo/kyvc_webcam1.jpg?1589316288166",
				Latitude:     40.437787,
				Longitude:    -121.5360307,
				FirstTime:    false,
				FirstSunrise: true,
				LastTime:     false,
				LastSunset:   true,
				FirstFlags:   firstSunrise,
				LastFlags:    lastSunset,
				Additional:   1,
				FolderPath:   "C:\\Users\\peter\\OneDrive\\Pictures\\Timelapse\\Kohm-Yah-mah-nee", // TODO: assemble path to avoid platform-specific path separator issues
			},
			day:     day1,
			wantErr: false,
			want:    day1Capture,
		},
		{name: "May28",
			tld: &TLDef{ // includes previous day's capture times
				Name:         "Kohm Yah-man-yeh",
				URL:          "https://www.nps.gov/webcams-lavo/kyvc_webcam1.jpg?1589316288166",
				Latitude:     40.4375635,
				Longitude:    -121.5357176,
				FirstTime:    false,
				FirstSunrise: true,
				LastTime:     false,
				LastSunset:   true,
				FirstFlags:   firstSunrise,
				LastFlags:    lastSunset,
				Additional:   1,
				FolderPath:   "C:\\Users\\peter\\OneDrive\\Pictures\\Timelapse\\Kohm-Yah-mah-nee", // TODO: assemble path to avoid platform-specific path separator issues
				CaptureTimes: day1Capture,
			},
			day:     day2,
			wantErr: false,
			want:    day2Capture,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.tld.SetCaptureTimes(tt.day); (err != nil) != tt.wantErr {
				t.Errorf("TLDef.SetCaptureTimes() error = %v, wantErr %v", err, tt.wantErr)
			} else {
				got := tt.tld.CaptureTimes
				if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("TLDef.SetCaptureTimes() got = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestTLDef_SetFirstCapture(t *testing.T) {
	// firstTime := time.Date(2020, 5, 27, 6, 0, 1, 0, loc)

	tests := []struct {
		name    string
		tld     TLDef
		wantErr bool
		want    CaptureTimes
	}{
		{name: "sunrise",
			tld: TLDef{
				FirstTime:      false,
				FirstSunrise:   true,
				FirstSunrise30: false,
				FirstSunrise60: false,
				FirstFlags:     firstSunrise,
				SunriseUTC:     sunrise,
			},
			wantErr: false,
			want:    CaptureTimes{sunrise},
		},
		{name: "sunrise30",
			tld: TLDef{
				FirstTime:      false,
				FirstSunrise:   false,
				FirstSunrise30: true,
				FirstSunrise60: false,
				FirstFlags:     firstSunrise30,
				SunriseUTC:     sunrise,
			},
			wantErr: false,
			want:    CaptureTimes{sunrise.Add(mins30)},
		},
		{name: "sunrise60",
			tld: TLDef{
				FirstTime:      false,
				FirstSunrise:   false,
				FirstSunrise30: false,
				FirstSunrise60: true,
				FirstFlags:     firstSunrise60,
				SunriseUTC:     sunrise,
			},
			wantErr: false,
			want:    CaptureTimes{sunrise.Add(mins60)},
		},
		// {name: "first time",
		// 	tld: TLDef{
		// 		FirstTime:    true,
		// 		FirstSunrise: false,
		// 	},
		// 	wantErr: false,
		// 	want:    CaptureTimes{firstTime},
		// },
		{name: "time and sunrise",
			tld: TLDef{
				FirstTime:    true,
				FirstSunrise: true,
				FirstFlags:   firstSunrise | firstTime,
			},
			wantErr: true,
		},
		{name: "sunrise and sunrise30 and sunrise60",
			tld: TLDef{
				FirstSunrise:   true,
				FirstSunrise30: true,
				FirstSunrise60: true,
				FirstFlags:     firstSunrise | firstSunrise30 | firstSunrise60,
			},
			wantErr: true,
		},
		{name: "none",
			tld: TLDef{
				FirstTime:      false,
				FirstSunrise:   false,
				FirstSunrise30: false,
				FirstSunrise60: false,
				FirstFlags:     0,
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.tld.SetFirstCapture(); (err != nil) != tt.wantErr {
				t.Errorf("TLDef.SetFirstCapture() error = %v, wantErr %v", err, tt.wantErr)
			} else {
				got := tt.tld.CaptureTimes
				if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("TLDef.SetFirstCapture() got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestTLDef_SetFirstLastFlags(t *testing.T) {
	baseTLD := newBaseTLD()
	baseTLD.FirstTime = false
	baseTLD.FirstSunrise = true
	baseTLD.FirstSunrise30 = false
	baseTLD.FirstSunrise60 = false
	baseTLD.LastTime = false
	baseTLD.LastSunset = true
	baseTLD.LastSunset30 = false
	baseTLD.LastSunset60 = false
	baseTLD.FirstFlags = 0
	baseTLD.LastFlags = 0

	firstLast30 := newBaseTLD()
	firstLast30.FirstSunrise = false
	firstLast30.FirstSunrise30 = true
	firstLast30.FirstSunrise60 = false
	firstLast30.LastTime = false
	firstLast30.LastSunset = false
	firstLast30.LastSunset30 = true
	firstLast30.LastSunset60 = false
	firstLast30.FirstFlags = 0
	firstLast30.LastFlags = 0

	firstLast60 := newBaseTLD()
	firstLast60.FirstSunrise = false
	firstLast60.FirstSunrise30 = false
	firstLast60.FirstSunrise60 = true
	firstLast60.LastTime = false
	firstLast60.LastSunset = false
	firstLast60.LastSunset30 = false
	firstLast60.LastSunset60 = true
	firstLast60.FirstFlags = 0
	firstLast60.LastFlags = 0

	tests := []struct {
		name      string
		tld       *TLDef
		wantFirst uint
		wantLast  uint
	}{
		{name: "sunrise sunset",
			tld:       &baseTLD,
			wantFirst: firstSunrise,
			wantLast:  lastSunset,
		},
		{name: "sunrise30 sunset30",
			tld:       &firstLast30,
			wantFirst: firstSunrise30,
			wantLast:  lastSunset30,
		},
		{name: "sunrise60 sunset60",
			tld:       &firstLast60,
			wantFirst: firstSunrise60,
			wantLast:  lastSunset60,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.tld.SetFirstLastFlags()
			if tt.tld.FirstFlags != tt.wantFirst {
				t.Errorf("TLDef.SetFirstLastFlags() got FirstFlags %v, want %v", tt.tld.FirstFlags, tt.wantFirst)
			}
			if tt.tld.LastFlags != tt.wantLast {
				t.Errorf("TLDef.SetFirstLastFlags() got LastFlags %v, want %v", tt.tld.LastFlags, tt.wantLast)
			}
		})
	}
}

func TestTLDef_SetAdditional(t *testing.T) {
	addTwoTheFirst := time.Date(2020, 5, 27, 10, 35, 32, 0, loc)
	addTwoTheSecond := time.Date(2020, 5, 27, 15, 31, 23, 0, loc)

	addThreeTheFirst := time.Date(2020, 5, 27, 9, 21, 34, 0, loc)
	addThreeTheSecond := solarNoon
	addThreeTheThird := time.Date(2020, 5, 27, 16, 45, 21, 0, loc)

	addFourTheFirst := time.Date(2020, 5, 27, 8, 37, 11, 0, loc)
	addFourTheSecond := time.Date(2020, 5, 27, 11, 34, 41, 0, loc)
	addFourTheThird := time.Date(2020, 5, 27, 14, 32, 11, 0, loc)
	addFourTheFourth := time.Date(2020, 5, 27, 17, 29, 41, 0, loc)

	addFiveTheFirst := time.Date(2020, 5, 27, 8, 7, 36, 0, loc)
	addFiveTheSecond := time.Date(2020, 5, 27, 10, 35, 31, 0, loc)
	addFiveTheThird := solarNoon
	addFiveTheFourth := time.Date(2020, 5, 27, 15, 31, 23, 0, loc)
	addFiveTheFifth := time.Date(2020, 5, 27, 17, 59, 18, 0, loc)

	tests := []struct {
		name string
		tld  TLDef
		want CaptureTimes
	}{
		{name: "add 0",
			tld: TLDef{
				Name:         "test",
				FirstSunrise: true,
				FirstTime:    false,
				LastTime:     false,
				LastSunset:   true,
				Additional:   0,
				FirstFlags:   firstSunrise,
				LastFlags:    lastSunset,
				SunriseUTC:   sunrise.In(time.UTC),
				SunsetUTC:    sunset.In(time.UTC),
				CaptureTimes: CaptureTimes{sunrise},
			},
			want: CaptureTimes{sunrise, sunset},
		},
		{name: "add 1", // always add solar noon when adding 1 capture
			tld: TLDef{
				Name:         "test",
				FirstSunrise: true,
				FirstTime:    false,
				LastTime:     false,
				LastSunset:   true,
				Additional:   1,
				FirstFlags:   firstSunrise,
				LastFlags:    lastSunset,
				SunriseUTC:   sunrise.In(time.UTC),
				SolarNoonUTC: solarNoon.In(time.UTC),
				SunsetUTC:    sunset.In(time.UTC),
				CaptureTimes: CaptureTimes{sunrise},
			},
			want: CaptureTimes{sunrise, solarNoon, sunset},
		},
		{name: "add 2",
			tld: TLDef{
				Name:         "test",
				FirstSunrise: true,
				FirstTime:    false,
				LastTime:     false,
				LastSunset:   true,
				Additional:   2,
				FirstFlags:   firstSunrise,
				LastFlags:    lastSunset,
				SunriseUTC:   sunrise.In(time.UTC),
				SunsetUTC:    sunset.In(time.UTC),
				CaptureTimes: CaptureTimes{sunrise},
			},
			want: CaptureTimes{sunrise, addTwoTheFirst, addTwoTheSecond, sunset},
		},
		{name: "add 3",
			tld: TLDef{
				Name:         "test",
				FirstSunrise: true,
				FirstTime:    false,
				LastTime:     false,
				LastSunset:   true,
				Additional:   3,
				FirstFlags:   firstSunrise,
				LastFlags:    lastSunset,
				SunriseUTC:   sunrise.In(time.UTC),
				SolarNoonUTC: solarNoon.In(time.UTC),
				SunsetUTC:    sunset.In(time.UTC),
				CaptureTimes: CaptureTimes{sunrise},
			},
			want: CaptureTimes{sunrise, addThreeTheFirst, addThreeTheSecond, addThreeTheThird, sunset},
		},
		{name: "add 4",
			tld: TLDef{
				Name:         "test",
				FirstSunrise: true,
				FirstTime:    false,
				LastTime:     false,
				LastSunset:   true,
				Additional:   4,
				FirstFlags:   firstSunrise,
				LastFlags:    lastSunset,
				SunriseUTC:   sunrise.In(time.UTC),
				SunsetUTC:    sunset.In(time.UTC),
				CaptureTimes: CaptureTimes{sunrise},
			},
			want: CaptureTimes{sunrise, addFourTheFirst, addFourTheSecond, addFourTheThird, addFourTheFourth, sunset}, // the last capture time is set seprately
		},
		{name: "add 5",
			tld: TLDef{
				Name:         "test",
				FirstSunrise: true,
				FirstTime:    false,
				LastTime:     false,
				LastSunset:   true,
				Additional:   5,
				FirstFlags:   firstSunrise,
				LastFlags:    lastSunset,
				SunriseUTC:   sunrise.In(time.UTC),
				SolarNoonUTC: solarNoon.In(time.UTC),
				SunsetUTC:    sunset.In(time.UTC),
				CaptureTimes: CaptureTimes{sunrise},
			},
			want: CaptureTimes{sunrise, addFiveTheFirst, addFiveTheSecond, addFiveTheThird, addFiveTheFourth, addFiveTheFifth, sunset}, // the last capture time is set seprately
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.tld.SetAdditional()
			got := tt.tld.CaptureTimes
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("TLDef.SetAdditional() got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTLDef_SetLastCapture(t *testing.T) {
	// lastTime := time.Date(2020, 5, 27, 21, 0, 1, 0, loc)

	tests := []struct {
		name    string
		tld     TLDef
		wantErr bool
		want    CaptureTimes
	}{
		{name: "sunset",
			tld: TLDef{
				LastTime:     false,
				LastSunset:   true,
				LastSunset30: false,
				LastSunset60: false,
				LastFlags:    lastSunset,
				SunsetUTC:    sunset,
			},
			wantErr: false,
			want:    CaptureTimes{sunset},
		},
		{name: "sunset30",
			tld: TLDef{
				LastTime:     false,
				LastSunset:   false,
				LastSunset30: true,
				LastSunset60: false,
				LastFlags:    lastSunset30,
				SunsetUTC:    sunset,
			},
			wantErr: false,
			want:    CaptureTimes{sunset.Add(-mins30)},
		},
		{name: "sunset60",
			tld: TLDef{
				LastTime:     false,
				LastSunset:   false,
				LastSunset30: false,
				LastSunset60: true,
				LastFlags:    lastSunset60,
				SunsetUTC:    sunset,
			},
			wantErr: false,
			want:    CaptureTimes{sunset.Add(-mins60)},
		},
		// {name: "last time",
		// 	tld: TLDef{
		// 		LastTime:     true,
		// 		LastSunset:   false,
		// 		LastSunset30: false,
		// 		LastSunset60: false,
		// 		LastFlags:    lastSunset,
		//		SunsetUTC:    sunset,
		// 	},
		// 	wantErr: false,
		// 	want:    CaptureTimes{lastTime},
		// },
		{name: "time and sunset",
			tld: TLDef{
				LastTime:   true,
				LastSunset: true,
				LastFlags:  lastSunset | lastTime,
			},
			wantErr: true,
		},
		{name: "sunset and sunset30 and sunset60",
			tld: TLDef{
				LastSunset:   true,
				LastSunset30: true,
				LastSunset60: true,
				LastFlags:    lastSunset | lastSunset30 | lastSunset60,
			},
			wantErr: true,
		},
		{name: "none",
			tld: TLDef{
				LastTime:     false,
				LastSunset:   false,
				LastSunset30: false,
				LastSunset60: false,
				LastFlags:    0,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.tld.SetLastCapture(); (err != nil) != tt.wantErr {
				t.Errorf("TLDef.SetLastCapture() error = %v, wantErr %v", err, tt.wantErr)
			} else {
				got := tt.tld.CaptureTimes
				if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("TLDef.SetLastCapture() got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestTLDef_UpdateNextCapture(t *testing.T) {
	// layout := "Jan 2 2006 15:04:05 -0700 MST"
	loc := time.Local
	baseTLD := newBaseTLD()

	unsortedTLD := TLDef{
		Name:         "Kohm Yah-man-yeh",
		URL:          "https://www.nps.gov/webcams-lavo/kyvc_webcam1.jpg?1589316288166",
		Latitude:     40.437787,
		Longitude:    -121.5360307,
		FirstTime:    false,
		FirstSunrise: true,
		LastTime:     false,
		LastSunset:   true,
		FirstFlags:   firstSunrise,
		LastFlags:    lastSunset,
		Additional:   3,
		FolderPath:   "C:\\Users\\peter\\OneDrive\\Pictures\\Timelapse\\zzTest", // TODO: assemble path to avoid platform-specific path separator issues
		CaptureTimes: CaptureTimes{sunset, sunset.Add(-mins30), sunrise.Add(mins60), sunrise, solarNoon},
		NextCapture:  0,
	}
	tests := []struct {
		name    string
		tld     *TLDef
		refDate time.Time
		want    int
		skip    bool
	}{
		{name: "first",
			tld:     &baseTLD,
			refDate: time.Date(2020, 5, 27, 0, 0, 0, 0, loc),
			want:    0,
		},
		{name: "middle",
			tld:     &baseTLD,
			refDate: sunrise.Add(mins30),
			want:    1,
		},
		{name: "last",
			tld:     &baseTLD,
			refDate: sunset.Add(-mins60),
			want:    2,
		},
		{name: "unsorted",
			tld:     &unsortedTLD,
			refDate: time.Date(2020, 5, 27, 12, 0, 1, 0, loc),
			want:    2,
		},
		{name: "new day",
			tld:     &baseTLD,
			refDate: sunset.Add(mins30),
			want:    0,
			skip:    true, // calls real TimeZoneDB API via SetCaptureTimes
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skip {
				t.Skip("calls real TimeZoneDB API")
			}
			tt.tld.UpdateNextCapture(tt.refDate)
			if tt.tld.NextCapture != tt.want {
				t.Errorf("UpdateNextCapture() got %d, want %d", tt.tld.NextCapture, tt.want)
			}
		})
	}
}

func TestTLDef_NextCaptureTime(t *testing.T) {
	t.Skip()
	tests := []struct {
		name string
		tld  TLDef
		want time.Time
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tld.NextCaptureTime(); got != tt.want {
				t.Errorf("TLDef.NextCaptureTime() got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTLDef_IsTimeForCapture(t *testing.T) {
	t.Skip()
	tests := []struct {
		name string
		tld  TLDef
		want bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tld.IsTimeForCapture(); got != tt.want {
				t.Errorf("TLDef.IsTimeForCapture() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTLDef_TimeToSecond(t *testing.T) {
	t.Skip()
	tests := []struct {
		name     string
		testTime time.Time
		want     time.Time
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TimeToSecond(tt.testTime); got != tt.want {
				t.Errorf("TLDef.TimeToSecond() got %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_newMasterTLDefs(t *testing.T) {
	t.Skip()
	tests := []struct {
		name string
		want *masterTLDefs
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := newMasterTLDefs(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("newMasterTLDefs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_masterTLDefs_Read(t *testing.T) {
	t.Skip()
	type args struct {
		path string
	}
	tests := []struct {
		name    string
		mtld    masterTLDefs
		args    args
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.mtld.Read(tt.args.path); (err != nil) != tt.wantErr {
				t.Errorf("masterTLDefs.Read() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_masterTLDefs_Write(t *testing.T) {
	t.Skip()
	tests := []struct {
		name    string
		mtld    masterTLDefs
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.mtld.Write(); (err != nil) != tt.wantErr {
				t.Errorf("masterTLDefs.Write() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_masterTLDefs_Append(t *testing.T) {
	value1 := TLDef{
		Name:         "tldName1",
		URL:          "tldURL1",
		FirstTime:    true,
		FirstSunrise: false,
		LastTime:     true,
		LastSunset:   false,
		Additional:   1,
		FolderPath:   "tldPath1",
	}
	mtldOneValue := new(masterTLDefs)
	*mtldOneValue = append(*mtldOneValue, &value1)

	value2 := TLDef{
		Name:         "tldName2",
		URL:          "tldURL2",
		FirstTime:    false,
		FirstSunrise: true,
		LastTime:     false,
		LastSunset:   true,
		Additional:   2,
		FolderPath:   "tldPath2",
	}
	mtldTwoValues := new(masterTLDefs)
	*mtldTwoValues = append(*mtldOneValue, &value2)

	type test struct {
		name     string
		mtld     *masterTLDefs
		newTLD   *TLDef
		expected *masterTLDefs
	}

	var workingMTLD = new(masterTLDefs)

	tests := []test{
		{name: "append to empty",
			mtld:     workingMTLD,
			newTLD:   &value1,
			expected: mtldOneValue,
		},
		{name: "append second element",
			mtld:     workingMTLD,
			newTLD:   &value2,
			expected: mtldTwoValues,
		},
	}

	for _, tt := range tests {
		if err := tt.mtld.Append(tt.newTLD); err != nil {
			t.Fatal(err)
		}

		lenMTLD := len(*(tt.mtld))
		for i := range lenMTLD {
			got := (*tt.mtld)[i]
			want := (*tt.expected)[i]
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s, got %+v, want %+v", tt.name, got, want)
			}
		}
	}
}

func Test_masterTLDefs_Delete(t *testing.T) {
	firstMTLD := []*TLDef{
		{Name: "testMatch"},
		{Name: "noMatch1"},
		{Name: "noMatch2"},
	}
	middleMTLD := []*TLDef{
		{Name: "noMatch1"},
		{Name: "testMatch"},
		{Name: "noMatch2"},
	}
	lastMTLD := []*TLDef{
		{Name: "noMatch1"},
		{Name: "noMatch2"},
		{Name: "testMatch"},
	}
	resultMTLD := []*TLDef{
		{Name: "noMatch1"},
		{Name: "noMatch2"},
	}

	tests := []struct {
		name   string
		prefix string
		mtld   masterTLDefs
		want   masterTLDefs
	}{
		{name: "first",
			prefix: "test",
			mtld:   firstMTLD,
			want:   resultMTLD,
		},
		{name: "middle",
			prefix: "test",
			mtld:   middleMTLD,
			want:   resultMTLD,
		},
		{name: "last",
			prefix: "test",
			mtld:   lastMTLD,
			want:   resultMTLD,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var newMTLD *masterTLDefs = tt.mtld.Delete(tt.prefix)

			if len(*newMTLD) != len(tt.want) {
				t.Errorf("%s len(newMTLD) = %d, want %d [i.e., len(want)]", tt.name, len(*newMTLD), len(tt.want))
			}
			for i, tld := range *newMTLD {
				if tld.Name != tt.want[i].Name {
					t.Errorf("%s mtld element %d, got %+v, want %+v", tt.name, i, tld.Name, tt.want[i].Name)
				}
			}
		})
	}
}

func TestTLDef_GetSolarTimes(t *testing.T) {
	var testDate, testSunrise, testSunset, testSolarNoon time.Time
	var err error
	const (
		layoutDate        = "Jan 2 2006"
		layoutDateAndTime = "Jan 2 2006 3:04:05 PM"
	)

	if testDate, err = time.Parse(layoutDate, "May 27 2020"); err != nil {
		t.Fatalf("time.Parse: %v", err)
	}
	if testSunrise, err = time.Parse(layoutDateAndTime, "May 27 2020 12:38:27 PM"); err != nil {
		t.Fatalf("time.Parse: %v", err)
	}
	if testSunset, err = time.Parse(layoutDateAndTime, "May 28 2020 3:28:22 AM"); err != nil {
		t.Fatalf("time.Parse: %v", err)
	}
	if testSolarNoon, err = time.Parse(layoutDateAndTime, "May 27 2020 8:03:24 PM"); err != nil {
		t.Fatalf("time.Parse: %v", err)
	}

	tests := []struct {
		name    string
		tld     TLDef
		date    time.Time
		wantErr bool
		want    TLDef
	}{
		{name: "valid",
			tld: TLDef{
				URL:       "https://www.nps.gov/webcams-lavo/kyvc_webcam1.jpg?1589316288166",
				Latitude:  40.437787,
				Longitude: -121.5360307,
			},
			date:    testDate,
			wantErr: false,
			want: TLDef{
				SunriseUTC:   testSunrise,
				SunsetUTC:    testSunset,
				SolarNoonUTC: testSolarNoon,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if err = tt.tld.GetSolarTimes(tt.date); (err != nil) != tt.wantErr {
				t.Errorf("GetSolarTimes() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.tld.SunriseUTC != tt.want.SunriseUTC {
				t.Errorf("GetSolarTimes(), SunriseUTC got %+v, want %+v", tt.tld.SunriseUTC, tt.want.SunriseUTC)
			}
			if tt.tld.SunsetUTC != tt.want.SunsetUTC {
				t.Errorf("GetSolarTimes(), SunsetUTC got %+v, want %+v", tt.tld.SunsetUTC, tt.want.SunsetUTC)
			}
			if tt.tld.SolarNoonUTC != tt.want.SolarNoonUTC {
				t.Errorf("GetSolarTimes(), SolarNoonUTC got %+v, want %+v", tt.tld.SolarNoonUTC, tt.want.SolarNoonUTC)
			}
		})
	}
}

func TestTLDef_NewSSDayInfo(t *testing.T) {
	t.Skip()
	tests := []struct {
		name string
		tld  TLDef
		want bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tld.IsTimeForCapture(); got != tt.want {
				t.Errorf("TLDef.NewSSDayInfo() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSSDayInfo_buildQuery(t *testing.T) {
	t.Skip()
	tests := []struct {
		name string
		tld  TLDef
		want bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tld.IsTimeForCapture(); got != tt.want {
				t.Errorf("SSDayInfo.buildQuery() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTLDef_NewTimeZoneDB(t *testing.T) {
	t.Skip()
	tests := []struct {
		name string
		tld  TLDef
		want TimeZoneDB
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewTimeZoneDB(&tt.tld)
			if !reflect.DeepEqual(*got, tt.want) {
				t.Errorf("%s got %+v, want %+v", tt.name, got, tt.want)
			}
		})
	}
}

func TestTLDef_SetWebcamTZ(t *testing.T) {
	t.Skip()
	tests := []struct {
		name string
		tld  TLDef
		want bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tld.IsTimeForCapture(); got != tt.want {
				t.Errorf("TLDef.SetWebcamTZ() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTimeZoneDB_buildQuery(t *testing.T) {
	t.Skip()
	tests := []struct {
		name string
		tld  TLDef
		want bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tld.IsTimeForCapture(); got != tt.want {
				t.Errorf("TimeZoneDB.buildQuery() = %v, want %v", got, tt.want)
			}
		})
	}
}
