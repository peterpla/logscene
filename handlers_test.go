// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// handlers_test.go tests HTTP handlers using httptest.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/julienschmidt/httprouter"
)

// newTestServer builds a minimal server with in-memory dependencies.
// TempDirs are created before the cancel cleanup is registered so that, in
// LIFO order, cancel()+wg.Wait() fires before the dirs are removed — ensuring
// goroutines exit cleanly and don't hold file locks during cleanup.
func newTestServer(t *testing.T) *server {
	t.Helper()
	store := NewMemStorage()
	pathDir := t.TempDir()
	logDir := t.TempDir()
	baseDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	webcamCtx, webcamCancel := context.WithCancel(ctx)
	s := &server{
		router:       httprouter.New(),
		validate:     newValidator(),
		config:       &Config{Path: pathDir, LogDir: logDir, BaseDir: baseDir, PollSecs: 60, Port: "9999"},
		webcams:      newWebcams(),
		storage:      store,
		renderer:     NewLocalRenderer(),
		tz:           &fixedTimezoneClient{tz: "America/Los_Angeles"},
		solar:        &fixedSolarClient{times: laFixedSolar()},
		fetcher:      &mockImageFetcher{data: []byte("img"), contentType: "image/jpeg"},
		ctx:          ctx,
		cancel:       cancel,
		webcamCtx:    webcamCtx,
		webcamCancel: webcamCancel,
		startTime:    time.Now(),
	}
	// Registered last → runs first (LIFO): goroutines exit before temp dirs are removed.
	t.Cleanup(func() {
		cancel()
		s.webcamWg.Wait()
		s.wg.Wait()
	})
	s.router.GET("/status", s.handleStatus())
	s.router.GET("/next", s.handleNext())
	return s
}

// ---------------------------------------------------------------------------
// GET /status
// ---------------------------------------------------------------------------

func TestHandleStatus_empty(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	var resp struct {
		Status  string `json:"status"`
		Webcams int    `json:"webcams"`
		Uptime  string `json:"uptime"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status field: want %q, got %q", "ok", resp.Status)
	}
	if resp.Webcams != 0 {
		t.Errorf("webcams: want 0, got %d", resp.Webcams)
	}
}

func TestHandleStatus_withWebcams(t *testing.T) {
	srv := newTestServer(t)
	srv.mu.Lock()
	srv.webcams.Append(newWebcam())
	srv.webcams.Append(newWebcam())
	srv.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	var resp struct {
		Webcams int `json:"webcams"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Webcams != 2 {
		t.Errorf("webcams: want 2, got %d", resp.Webcams)
	}
}

// ---------------------------------------------------------------------------
// GET /next
// ---------------------------------------------------------------------------

func TestHandleNext_noCaptures(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/next", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	var resp struct {
		Status string `json:"status"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status != "no captures scheduled" {
		t.Errorf("want 'no captures scheduled', got %q", resp.Status)
	}
}

func TestHandleNext_withCapture(t *testing.T) {
	srv := newTestServer(t)
	wc := newWebcam()
	wc.Name = "My Cam"
	wc.NextCaptureAt = time.Now().Add(time.Hour)
	srv.mu.Lock()
	srv.webcams.Append(wc)
	srv.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/next", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	var resp struct {
		Webcam      string `json:"webcam"`
		NextCapture string `json:"next_capture"`
		In          string `json:"in"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Webcam != "My Cam" {
		t.Errorf("webcam: want %q, got %q", "My Cam", resp.Webcam)
	}
	if resp.NextCapture == "" {
		t.Error("next_capture should not be empty")
	}
}

// ---------------------------------------------------------------------------
// GET /logs

func TestHandleLogs_notFound(t *testing.T) {
	srv := newTestServer(t)
	srv.router.GET("/logs", srv.handleLogs())
	// LogDir points at an empty temp dir — no log file exists yet.

	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}

func TestHandleLogs_returnsLines(t *testing.T) {
	srv := newTestServer(t)
	srv.router.GET("/logs", srv.handleLogs())

	// Write a fake log file named for today.
	logPath := filepath.Join(srv.config.LogDir, "logscene-"+time.Now().Format("2006-01-02")+".log")
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, line := range []string{"line1", "line3", "line5"} {
		if !strings.Contains(body, line) {
			t.Errorf("response missing %q: %s", line, body)
		}
	}
}

func TestHandleLogs_nParam(t *testing.T) {
	srv := newTestServer(t)
	srv.router.GET("/logs", srv.handleLogs())

	// Write 10 lines; request only the last 3.
	logPath := filepath.Join(srv.config.LogDir, "logscene-"+time.Now().Format("2006-01-02")+".log")
	var sb strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&sb, "line%d\n", i)
	}
	os.WriteFile(logPath, []byte(sb.String()), 0644)

	req := httptest.NewRequest(http.MethodGet, "/logs?n=3", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	body := w.Body.String()
	// Should contain lines 8-10, not line1.
	if strings.Contains(body, "line1\n") {
		t.Errorf("response should not contain line1 when n=3: %s", body)
	}
	if !strings.Contains(body, "line10") {
		t.Errorf("response should contain line10: %s", body)
	}
}

// POST /new — validation
// ---------------------------------------------------------------------------

// POST /new — field-parse error paths
// ---------------------------------------------------------------------------

func TestHandleNew_invalidLatitude(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("name", "Test Cam")
	form.Set("webcamUrl", "http://example.com/cam.jpg")
	form.Set("latitude", "notanumber")
	form.Set("longitude", "-122.42")
	form.Set("intervalMinutes", "15")
	form.Set("folder", "test-cam")
	form.Set("firstSunrise", "on")
	form.Set("lastSunset", "on")

	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid latitude: want 400, got %d", w.Code)
	}
}

func TestHandleNew_invalidLongitude(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("name", "Test Cam")
	form.Set("webcamUrl", "http://example.com/cam.jpg")
	form.Set("latitude", "37.77")
	form.Set("longitude", "notanumber")
	form.Set("intervalMinutes", "15")
	form.Set("folder", "test-cam")
	form.Set("firstSunrise", "on")
	form.Set("lastSunset", "on")

	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid longitude: want 400, got %d", w.Code)
	}
}

func TestHandleNew_intervalNonNumeric(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("name", "Test Cam")
	form.Set("webcamUrl", "http://example.com/cam.jpg")
	form.Set("latitude", "37.77")
	form.Set("longitude", "-122.42")
	form.Set("intervalMinutes", "abc") // not an integer
	form.Set("folder", "test-cam")
	form.Set("firstSunrise", "on")
	form.Set("lastSunset", "on")

	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("non-numeric interval: want 400, got %d", w.Code)
	}
}

func TestHandleNew_intervalZero(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("name", "Test Cam")
	form.Set("webcamUrl", "http://example.com/cam.jpg")
	form.Set("latitude", "37.77")
	form.Set("longitude", "-122.42")
	form.Set("intervalMinutes", "0") // fails min=1 validation
	form.Set("folder", "test-cam")
	form.Set("firstSunrise", "on")
	form.Set("lastSunset", "on")

	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("interval=0: want 400, got %d", w.Code)
	}
}

// POST /new — validation (existing tests below)
// ---------------------------------------------------------------------------

func TestHandleNew_missingName(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("webcamUrl", "http://example.com/cam.jpg")
	form.Set("latitude", "37.77")
	form.Set("longitude", "-122.42")
	form.Set("intervalMinutes", "15")
	form.Set("folder", "test-cam")
	form.Set("firstSunrise", "on")
	form.Set("lastSunset", "on")

	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("missing name: want 400, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// GET /
// ---------------------------------------------------------------------------

func TestHandleHome(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}
	srv.router.GET("/", srv.handleHome())

	// Add a webcam so the dashboard has a card to render.
	wc := newWebcam()
	wc.Name = "Test Camera"
	srv.mu.Lock()
	srv.webcams.Append(wc)
	srv.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d\nbody: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// Webcam name and Render button appear on the dashboard.
	for _, want := range []string{"Test Camera", "Render"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}

	// Dashboard nav link is active; others are not.
	if !strings.Contains(body, `class="nav-link active">Dashboard`) {
		t.Errorf("Dashboard nav link should be active")
	}
	for _, link := range []string{"Add Webcam", "Logs"} {
		if strings.Contains(body, `class="nav-link active">`+link) {
			t.Errorf("%s nav link should not be active", link)
		}
	}
}

// parseDirectShowVideoDevices
// ---------------------------------------------------------------------------

func TestParseDirectShowVideoDevices(t *testing.T) {
	newFmt := `ffmpeg version 8.1.1
[in#0 @ 0xc0ffee] "Microsoft Modern Webcam" (video)
[in#0 @ 0xc0ffee]   Alternative name "@device_pnp_\\?\usb#..."
[in#0 @ 0xc0ffee] Could not enumerate audio only devices (or none found).
Error opening input file dummy.`

	oldFmt := `[dshow @ 0xc0ffee] DirectShow video devices (some may be both video and audio devices)
[dshow @ 0xc0ffee] "Logitech C920"
[dshow @ 0xc0ffee]   Alternative name "@device_pnp_\\?\usb#..."
[dshow @ 0xc0ffee] "OBS Virtual Camera"
[dshow @ 0xc0ffee] DirectShow audio devices
[dshow @ 0xc0ffee] "Microphone (Realtek Audio)"`

	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"new format single device", newFmt, []string{"Microsoft Modern Webcam"}},
		{"old format multiple devices", oldFmt, []string{"Logitech C920", "OBS Virtual Camera"}},
		{"empty output", "", []string{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDirectShowVideoDevices([]byte(tc.in))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("device[%d]: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// GET /devices
// ---------------------------------------------------------------------------

func TestHandleDevices(t *testing.T) {
	srv := newTestServer(t)
	srv.router.GET("/devices", srv.handleDevices())

	req := httptest.NewRequest(http.MethodGet, "/devices", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	// Must be valid JSON and an array (possibly empty when ffmpeg absent).
	var devices []string
	if err := json.NewDecoder(w.Body).Decode(&devices); err != nil {
		t.Fatalf("response is not a JSON string array: %v", err)
	}
}

// POST /new — success path
// ---------------------------------------------------------------------------

func TestHandleNew_success(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("name", "Test Cam")
	form.Set("webcamUrl", "http://example.com/cam.jpg")
	form.Set("latitude", "37.77")
	form.Set("longitude", "-122.42")
	form.Set("intervalMinutes", "15")
	form.Set("folder", "test-cam")
	form.Set("firstSunrise", "on")
	form.Set("lastSunset", "on")

	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("want 303, got %d\nbody: %s", w.Code, w.Body.String())
	}
	srv.mu.RLock()
	n := len(*srv.webcams)
	srv.mu.RUnlock()
	if n != 1 {
		t.Errorf("want 1 webcam appended, got %d", n)
	}
}

func TestHandleNew_usbMissingDeviceName(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("name", "USB Cam")
	form.Set("sourceType", "usb")
	// deviceName intentionally omitted
	form.Set("latitude", "37.77")
	form.Set("longitude", "-122.42")
	form.Set("intervalMinutes", "15")
	form.Set("folder", "usb-cam")
	form.Set("firstSunrise", "on")
	form.Set("lastSunset", "on")

	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 when USB source has no deviceName, got %d", w.Code)
	}
}

func TestHandleNew_folderTraversal(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	for _, folder := range []string{"../evil", "../../etc/passwd", "..", "good/../../../evil"} {
		form := url.Values{}
		form.Set("name", "Test Cam")
		form.Set("webcamUrl", "http://example.com/cam.jpg")
		form.Set("latitude", "37.77")
		form.Set("longitude", "-122.42")
		form.Set("intervalMinutes", "15")
		form.Set("folder", folder)
		form.Set("firstSunrise", "on")
		form.Set("lastSunset", "on")

		req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("folder %q: want 400, got %d", folder, w.Code)
		}
	}
}

// ---------------------------------------------------------------------------
// GET /info
// ---------------------------------------------------------------------------

func TestHandleInfo(t *testing.T) {
	srv := newTestServer(t)
	srv.router.GET("/info", srv.handleInfo())

	req := httptest.NewRequest(http.MethodGet, "/info", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}

	var resp struct {
		Version   string `json:"version"`
		BuildDate string `json:"build_date"`
		GoVersion string `json:"go_version"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Version == "" {
		t.Error("version should not be empty")
	}
	if resp.BuildDate == "" {
		t.Error("build_date should not be empty")
	}
	if resp.GoVersion == "" {
		t.Error("go_version should not be empty")
	}
}

// ---------------------------------------------------------------------------
// POST /render
// ---------------------------------------------------------------------------

func TestHandleRender_success(t *testing.T) {
	srv := newTestServer(t)
	mr := &mockRenderer{}
	srv.renderer = mr
	srv.router.POST("/render", srv.handleRender())

	body := `{"folder":"kohm-yah-mah-nee","output":"output.mp4"}`
	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
	var resp struct {
		Status string `json:"status"`
		Output string `json:"output"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "rendering" {
		t.Errorf("status: want %q, got %q", "rendering", resp.Status)
	}
	if resp.Output != "output.mp4" {
		t.Errorf("output: want %q, got %q", "output.mp4", resp.Output)
	}
}

func TestHandleRender_stridePassedThrough(t *testing.T) {
	srv := newTestServer(t)
	mr := &mockRenderer{called: make(chan struct{})}
	srv.renderer = mr
	srv.router.POST("/render", srv.handleRender())

	body := `{"folder":"f","output":"out.mp4","stride":3}`
	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	select {
	case <-mr.called:
	case <-time.After(time.Second):
		t.Fatal("Render was not called within 1s")
	}
	if mr.lastOpts.Stride != 3 {
		t.Errorf("Stride: want 3, got %d", mr.lastOpts.Stride)
	}
}

func TestHandleRender_badJSON(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/render", srv.handleRender())

	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestHandleRender_missingFields(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/render", srv.handleRender())

	body := `{"folder":"kohm-yah-mah-nee"}` // output field missing
	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// POST /new — validation (continued)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// POST /render — trial enforcement
// ---------------------------------------------------------------------------

func TestHandleRender_trialReadOnly(t *testing.T) {
	srv := newTestServer(t)
	srv.trial = TrialReadOnly
	srv.router.POST("/render", srv.handleRender())

	body := `{"folder":"cam","output":"out.mp4"}`
	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("TrialReadOnly: want 403, got %d", w.Code)
	}
}

func TestHandleRender_trialGraceRender_firstCall(t *testing.T) {
	// Ensure a clean slate in the registry before and after the test.
	writeLastRenderDate("") //nolint:errcheck
	t.Cleanup(func() { writeLastRenderDate("") }) //nolint:errcheck

	srv := newTestServer(t)
	srv.trial = TrialGraceRender
	srv.renderer = &mockRenderer{}
	srv.router.POST("/render", srv.handleRender())

	body := `{"folder":"cam","output":"out.mp4"}`
	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GraceRender first call: want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleRender_trialGraceRender_secondCallSameDay(t *testing.T) {
	today := time.Now().Format("2006-01-02")
	writeLastRenderDate(today) //nolint:errcheck
	t.Cleanup(func() { writeLastRenderDate("") }) //nolint:errcheck

	srv := newTestServer(t)
	srv.trial = TrialGraceRender
	srv.router.POST("/render", srv.handleRender())

	body := `{"folder":"cam","output":"out.mp4"}`
	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("GraceRender second call same day: want 403, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// POST /new — trial enforcement
// ---------------------------------------------------------------------------

func TestHandleNew_trialCapturesStopped(t *testing.T) {
	srv := newTestServer(t)
	srv.trial = TrialGraceRender
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("name", "Test Cam")
	form.Set("webcamUrl", "http://example.com/cam.jpg")
	form.Set("latitude", "37.77")
	form.Set("longitude", "-122.42")
	form.Set("intervalMinutes", "15")
	form.Set("folder", "test-cam")
	form.Set("firstSunrise", "on")
	form.Set("lastSunset", "on")

	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("capturesStopped: want 403, got %d", w.Code)
	}
}

func TestHandleNew_trialSecondWebcamBlocked(t *testing.T) {
	srv := newTestServer(t)
	srv.trial = TrialActive
	srv.mu.Lock()
	srv.webcams.Append(newWebcam())
	srv.mu.Unlock()
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("name", "Second Cam")
	form.Set("webcamUrl", "http://example.com/cam.jpg")
	form.Set("latitude", "37.77")
	form.Set("longitude", "-122.42")
	form.Set("intervalMinutes", "15")
	form.Set("folder", "second-cam")
	form.Set("firstSunrise", "on")
	form.Set("lastSunset", "on")

	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("second webcam in trial: want 403, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// POST /reload
// ---------------------------------------------------------------------------

// writeEmptyConfig writes an empty logscene.json to the server's config.Path
// so handleReload has a valid file to read.
func writeEmptyConfig(t *testing.T, srv *server) {
	t.Helper()
	if err := newWebcams().Write(srv.config.Path, srv.validate); err != nil {
		t.Fatalf("writeEmptyConfig: %v", err)
	}
}

func TestHandleReload_success(t *testing.T) {
	srv := newTestServer(t)
	writeEmptyConfig(t, srv)
	srv.router.POST("/reload", srv.handleReload())

	req := httptest.NewRequest(http.MethodPost, "/reload", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Status  string `json:"status"`
		Webcams int    `json:"webcams"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "reloaded" {
		t.Errorf("status: want %q, got %q", "reloaded", resp.Status)
	}
	if resp.Webcams != 0 {
		t.Errorf("webcams: want 0, got %d", resp.Webcams)
	}
}

func TestHandleReload_badConfig(t *testing.T) {
	srv := newTestServer(t)
	// Write invalid JSON where logscene.json should be.
	badPath := fmt.Sprintf("%s/%s", srv.config.Path, masterFile)
	if err := os.WriteFile(badPath, []byte("not-json"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	srv.router.POST("/reload", srv.handleReload())

	req := httptest.NewRequest(http.MethodPost, "/reload", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("bad config: want 400, got %d", w.Code)
	}
}

func TestHandleReload_capturesStopped(t *testing.T) {
	srv := newTestServer(t)
	srv.trial = TrialGraceRender
	writeEmptyConfig(t, srv)
	srv.router.POST("/reload", srv.handleReload())

	req := httptest.NewRequest(http.MethodPost, "/reload", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Status string `json:"status"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status != "reloaded" {
		t.Errorf("status: want %q, got %q", "reloaded", resp.Status)
	}
}

// ---------------------------------------------------------------------------
// POST /new — validation (continued)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// POST /new — unknown sourceType (validateWebcamSource default branch)
// ---------------------------------------------------------------------------

func TestHandleNew_urlSourceMissingURL(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("name", "Test Cam")
	// sourceType defaults to "url" when omitted; webcamUrl intentionally absent.
	form.Set("latitude", "37.77")
	form.Set("longitude", "-122.42")
	form.Set("intervalMinutes", "15")
	form.Set("folder", "test-cam")
	form.Set("firstSunrise", "on")
	form.Set("lastSunset", "on")

	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("url source with no URL: want 400, got %d", w.Code)
	}
}

func TestHandleNew_unknownSourceType(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("name", "Test Cam")
	form.Set("sourceType", "bogus")
	form.Set("latitude", "37.77")
	form.Set("longitude", "-122.42")
	form.Set("intervalMinutes", "15")
	form.Set("folder", "test-cam")
	form.Set("firstSunrise", "on")
	form.Set("lastSunset", "on")

	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("unknown sourceType: want 400, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// POST /new — LocalStorage pre-creates webcam directory
// ---------------------------------------------------------------------------

func TestHandleNew_localStorageMkdir(t *testing.T) {
	srv := newTestServer(t)
	srv.storage = NewLocalStorage() // switch so the MkdirAll block runs
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("name", "Test Cam")
	form.Set("webcamUrl", "http://example.com/cam.jpg")
	form.Set("latitude", "37.77")
	form.Set("longitude", "-122.42")
	form.Set("intervalMinutes", "15")
	form.Set("folder", "local-cam")
	form.Set("firstSunrise", "on")
	form.Set("lastSunset", "on")

	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("want 303, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(srv.config.BaseDir, "local-cam")); err != nil {
		t.Errorf("expected webcam directory to be created: %v", err)
	}
}

// ---------------------------------------------------------------------------
// POST /new — validation (continued)
// ---------------------------------------------------------------------------

func TestHandleNew_multipleFirstFlags(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("name", "Test Cam")
	form.Set("webcamUrl", "http://example.com/cam.jpg")
	form.Set("latitude", "37.77")
	form.Set("longitude", "-122.42")
	form.Set("intervalMinutes", "15")
	form.Set("folder", "test-cam")
	form.Set("firstSunrise", "on")
	form.Set("firstSunrise30", "on") // two first flags — invalid
	form.Set("lastSunset", "on")

	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("multiple first flags: want 400, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// HTML test helpers
// ---------------------------------------------------------------------------

// assertHTML fails the test if fragment is absent from body.
func assertHTML(t *testing.T, body, fragment string) {
	t.Helper()
	if !strings.Contains(body, fragment) {
		t.Errorf("body missing HTML:\n  %s", fragment)
	}
}

// assertNoHTML fails the test if fragment is present in body.
func assertNoHTML(t *testing.T, body, fragment string) {
	t.Helper()
	if strings.Contains(body, fragment) {
		t.Errorf("body should not contain HTML:\n  %s", fragment)
	}
}

// getBody is a convenience helper that executes a GET against the router and
// returns the response code and body string.
func getBody(t *testing.T, srv *server, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// ---------------------------------------------------------------------------
// GET /new — UI tests
// ---------------------------------------------------------------------------

func TestHandleGetNew_activeForm(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}
	srv.router.GET("/new", srv.handleGetNew())

	code, body := getBody(t, srv, "/new")
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	assertHTML(t, body, `<form`)
	assertHTML(t, body, `Remote Camera`)
	assertHTML(t, body, `USB Webcam`)
	assertHTML(t, body, `IP Camera`)
	assertHTML(t, body, `Friendly Name`)
	assertHTML(t, body, `Find Coordinates`)
	assertHTML(t, body, `name="intervalMinutes"`)
	assertHTML(t, body, `Test shot`)
	assertHTML(t, body, `class="nav-link active">Add Webcam`)
	assertNoHTML(t, body, `class="alert alert-danger"`)
}

func TestHandleGetNew_capturesStopped(t *testing.T) {
	srv := newTestServer(t)
	srv.trial = TrialGraceRender
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}
	srv.router.GET("/new", srv.handleGetNew())

	_, body := getBody(t, srv, "/new")
	assertNoHTML(t, body, `<form`)
	assertHTML(t, body, `Upgrade to LogScene Pro`)
	assertHTML(t, body, `nav-link-disabled`)
}

func TestHandleGetNew_trialWarning(t *testing.T) {
	srv := newTestServer(t)
	srv.trial = TrialWarning
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}
	srv.router.GET("/new", srv.handleGetNew())

	_, body := getBody(t, srv, "/new")
	assertHTML(t, body, `alert-warning`)
	assertHTML(t, body, `<form`) // form still shown during warning
}

func TestHandleGetNew_navLinkActive(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}
	srv.router.GET("/new", srv.handleGetNew())

	_, body := getBody(t, srv, "/new")
	assertHTML(t, body, `class="nav-link active">Add Webcam`)
	assertNoHTML(t, body, `class="nav-link active">Dashboard`)
}

// ---------------------------------------------------------------------------
// GET / — dashboard UI tests
// ---------------------------------------------------------------------------

func TestHandleHome_emptyState(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}
	srv.router.GET("/", srv.handleHome())

	_, body := getBody(t, srv, "/")
	assertHTML(t, body, `No webcams configured`)
	assertHTML(t, body, `Add your first webcam`)
	assertNoHTML(t, body, `card-title`)
}

func TestHandleHome_webcamCard_active(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}
	srv.router.GET("/", srv.handleHome())

	wc := newWebcam()
	wc.Name = "Glacier View"
	wc.Folder = "glacier"
	srv.mu.Lock()
	srv.webcams.Append(wc)
	srv.mu.Unlock()

	_, body := getBody(t, srv, "/")
	assertHTML(t, body, `Glacier View`)
	assertHTML(t, body, `bg-success`)
	assertHTML(t, body, `Active`)
	assertHTML(t, body, `glacier`)
	assertHTML(t, body, `Render`)
}

func TestHandleHome_webcamCard_disabled(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}
	srv.router.GET("/", srv.handleHome())

	wc := newWebcam()
	wc.Name = "Offline Cam"
	wc.Disabled = true
	srv.mu.Lock()
	srv.webcams.Append(wc)
	srv.mu.Unlock()

	_, body := getBody(t, srv, "/")
	assertHTML(t, body, `bg-secondary`)
	assertHTML(t, body, `Disabled`)
}

func TestHandleHome_webcamCard_error(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}
	srv.router.GET("/", srv.handleHome())

	wc := newWebcam()
	wc.Name = "Flaky Cam"
	wc.FirstFailure = time.Now().Add(-10 * time.Minute)
	srv.mu.Lock()
	srv.webcams.Append(wc)
	srv.mu.Unlock()

	_, body := getBody(t, srv, "/")
	assertHTML(t, body, `bg-warning`)
	assertHTML(t, body, `Error`)
}

func TestHandleHome_renderButton_noCaptures(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}
	srv.router.GET("/", srv.handleHome())

	wc := newWebcam()
	wc.Name = "New Cam"
	// NextCapture=0, CaptureTimes empty → CapturesToday=0
	srv.mu.Lock()
	srv.webcams.Append(wc)
	srv.mu.Unlock()

	_, body := getBody(t, srv, "/")
	assertHTML(t, body, `disabled title="No captures yet today"`)
	assertNoHTML(t, body, `btn-outline-primary"`)
}

func TestHandleHome_renderButton_withCaptures(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}
	srv.router.GET("/", srv.handleHome())

	wc := newWebcam()
	wc.Name = "Active Cam"
	wc.IntervalMinutes = 60
	wc.DayFirst = time.Now().Add(-2 * time.Hour)
	wc.DayLast = time.Now().Add(2 * time.Hour)
	wc.NextCaptureAt = time.Now().Add(time.Hour) // 2 intervals past DayFirst → CapturesToday=2
	srv.mu.Lock()
	srv.webcams.Append(wc)
	srv.mu.Unlock()

	_, body := getBody(t, srv, "/")
	assertHTML(t, body, `btn-outline-primary`)
	assertNoHTML(t, body, `disabled title="No captures yet today"`)
}

func TestHandleHome_trialWarning(t *testing.T) {
	srv := newTestServer(t)
	srv.trial = TrialWarning
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}
	srv.router.GET("/", srv.handleHome())

	_, body := getBody(t, srv, "/")
	assertHTML(t, body, `alert-warning`)
	assertHTML(t, body, `free trial ends today`)
	assertNoHTML(t, body, `alert-danger`)
}

func TestHandleHome_trialGraceRender(t *testing.T) {
	srv := newTestServer(t)
	srv.trial = TrialGraceRender
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}
	srv.router.GET("/", srv.handleHome())

	_, body := getBody(t, srv, "/")
	assertHTML(t, body, `Captures have stopped`)
	assertHTML(t, body, `nav-link-disabled`)
	assertHTML(t, body, `disabled title="Upgrade to add webcams"`)
}

func TestHandleHome_trialReadOnly(t *testing.T) {
	srv := newTestServer(t)
	srv.trial = TrialReadOnly
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}
	srv.router.GET("/", srv.handleHome())

	wc := newWebcam()
	wc.Name = "Old Cam"
	wc.IntervalMinutes = 60
	wc.DayFirst = time.Now().Add(-3 * time.Hour)
	wc.DayLast = time.Now().Add(2 * time.Hour)
	wc.NextCaptureAt = time.Now().Add(time.Hour)
	srv.mu.Lock()
	srv.webcams.Append(wc)
	srv.mu.Unlock()

	_, body := getBody(t, srv, "/")
	assertHTML(t, body, `trial has ended`)
	assertHTML(t, body, `disabled title="Upgrade to render"`)
}

// ---------------------------------------------------------------------------
// webcamCard — "Done for today" branch
// ---------------------------------------------------------------------------

func TestHandleHome_webcamCard_doneForToday(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}
	srv.router.GET("/", srv.handleHome())

	wc := newWebcam()
	wc.Name = "Done Cam"
	wc.IntervalMinutes = 60
	wc.DayFirst = time.Now().Add(-8 * time.Hour)
	wc.DayLast = time.Now().Add(-time.Minute) // last capture was in the past
	// NextCaptureAt left zero → "Done for today" path in webcamCard
	srv.mu.Lock()
	srv.webcams.Append(wc)
	srv.mu.Unlock()

	_, body := getBody(t, srv, "/")
	assertHTML(t, body, `Done for today`)
}

// TestWebcamCard_nextCaptureIncludesTimezone verifies that the displayed time
// carries the webcam's timezone abbreviation (e.g. "PDT") so the dashboard is
// unambiguous when the viewer is in a different zone.
func TestWebcamCard_nextCaptureIncludesTimezone(t *testing.T) {
	loc, _ := time.LoadLocation("America/Los_Angeles")
	wc := newWebcam()
	wc.IntervalMinutes = 15
	wc.WebcamLoc = loc
	// 19:00 UTC = 12:00 PM PDT (UTC-7, summer / PDT offset)
	wc.NextCaptureAt = time.Date(2026, 6, 6, 19, 0, 0, 0, time.UTC)
	wc.DayFirst = time.Date(2026, 6, 6, 13, 0, 0, 0, time.UTC) // 06:00 PDT
	wc.DayLast = time.Date(2026, 6, 7, 3, 0, 0, 0, time.UTC)   // 20:00 PDT

	d := webcamCard(wc)

	want := "12:00 PM PDT"
	if d.NextCapture != want {
		t.Errorf("NextCapture: want %q, got %q", want, d.NextCapture)
	}
}

// ---------------------------------------------------------------------------
// GET /latlong
// ---------------------------------------------------------------------------

func TestHandleGetLatlong(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}
	srv.router.GET("/latlong", srv.handleGetLatlong())

	_, body := getBody(t, srv, "/latlong")
	assertHTML(t, body, "Find Coordinates")
}

// ---------------------------------------------------------------------------
// POST /probe
// ---------------------------------------------------------------------------

func TestHandleProbe_badJSON(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/probe", srv.handleProbe())

	req := httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestHandleProbe_nonURLSourceType(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/probe", srv.handleProbe())

	body := `{"url":"http://example.com/cam.jpg","sourceType":"usb"}`
	req := httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	var resp struct{ Error string `json:"error"` }
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error == "" {
		t.Error("expected error response for non-URL sourceType")
	}
}

func TestHandleProbe_invalidURLPrefix(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/probe", srv.handleProbe())

	body := `{"url":"ftp://example.com/cam.jpg","sourceType":"url"}`
	req := httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	var resp struct{ Error string `json:"error"` }
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error == "" {
		t.Error("expected error response for non-http URL")
	}
}

func TestHandleProbe_fetchError(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/probe", srv.handleProbe())

	// Point at a closed server so the fetch fails.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead.Close()

	body := fmt.Sprintf(`{"url":"%s/cam.jpg","sourceType":"url"}`, dead.URL)
	req := httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	var resp struct{ Error string `json:"error"` }
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error == "" {
		t.Error("expected error response for unreachable camera")
	}
}

func TestHandleProbe_success(t *testing.T) {
	camServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		fmt.Fprint(w, "fake-image-data")
	}))
	defer camServer.Close()

	srv := newTestServer(t)
	srv.router.POST("/probe", srv.handleProbe())

	body := fmt.Sprintf(`{"url":"%s/cam.jpg","sourceType":"url"}`, camServer.URL)
	req := httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	var resp struct {
		Bytes int64  `json:"bytes"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != "" {
		t.Errorf("unexpected error: %q", resp.Error)
	}
	if resp.Bytes == 0 {
		t.Error("expected non-zero bytes from probe")
	}
}
