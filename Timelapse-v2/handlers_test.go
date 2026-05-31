package main

// handlers_test.go tests HTTP handlers using httptest.

import (
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

	"github.com/go-playground/validator/v10"
	"github.com/julienschmidt/httprouter"
)

// newTestServer builds a minimal server with in-memory dependencies.
func newTestServer(t *testing.T) *server {
	t.Helper()
	store := NewMemStorage()
	s := &server{
		router:    httprouter.New(),
		validate:  validator.New(),
		config:    &Config{Path: t.TempDir(), LogDir: t.TempDir(), BaseDir: t.TempDir(), PollSecs: 60, Port: "9999"},
		webcams:   newWebcams(),
		storage:   store,
		renderer:  NewLocalRenderer(store),
		tz:        &fixedTimezoneClient{tz: "America/Los_Angeles"},
		solar:     &fixedSolarClient{times: laFixedSolar()},
		fetcher:   &mockImageFetcher{data: []byte("img"), contentType: "image/jpeg"},
		startTime: time.Now(),
	}
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
	wc.CaptureTimes = []time.Time{time.Now().Add(time.Hour)}
	wc.NextCapture = 0
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
	logPath := filepath.Join(srv.config.LogDir, "timelapse-"+time.Now().Format("2006-01-02")+".log")
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
	logPath := filepath.Join(srv.config.LogDir, "timelapse-"+time.Now().Format("2006-01-02")+".log")
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

func TestHandleNew_missingName(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("webcamUrl", "http://example.com/cam.jpg")
	form.Set("latitude", "37.77")
	form.Set("longitude", "-122.42")
	form.Set("additional", "0")
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

func TestHandleNew_invalidAdditional(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("name", "Test Cam")
	form.Set("webcamUrl", "http://example.com/cam.jpg")
	form.Set("latitude", "37.77")
	form.Set("longitude", "-122.42")
	form.Set("additional", "99")
	form.Set("folder", "test-cam")
	form.Set("firstSunrise", "on")
	form.Set("lastSunset", "on")

	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid additional: want 400, got %d", w.Code)
	}
}

func TestHandleNew_multipleFirstFlags(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("name", "Test Cam")
	form.Set("webcamUrl", "http://example.com/cam.jpg")
	form.Set("latitude", "37.77")
	form.Set("longitude", "-122.42")
	form.Set("additional", "0")
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
