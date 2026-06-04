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

	"github.com/go-playground/validator/v10"
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
		validate:     validator.New(),
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
	form.Set("additional", "0")
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
	form.Set("additional", "0")
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

func TestHandleNew_additionalNonNumeric(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("name", "Test Cam")
	form.Set("webcamUrl", "http://example.com/cam.jpg")
	form.Set("latitude", "37.77")
	form.Set("longitude", "-122.42")
	form.Set("additional", "abc") // not an integer → strconv.Atoi error
	form.Set("folder", "test-cam")
	form.Set("firstSunrise", "on")
	form.Set("lastSunset", "on")

	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("non-numeric additional: want 400, got %d", w.Code)
	}
}

func TestHandleNew_additionalOutOfRange(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("name", "Test Cam")
	form.Set("webcamUrl", "http://example.com/cam.jpg")
	form.Set("latitude", "37.77")
	form.Set("longitude", "-122.42")
	form.Set("additional", "-1") // valid int, but < 0 → range check fails
	form.Set("folder", "test-cam")
	form.Set("firstSunrise", "on")
	form.Set("lastSunset", "on")

	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("additional out of range: want 400, got %d", w.Code)
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
	form.Set("additional", "48") // valid int, but > 47 → range check fails
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

func TestHandleNew_additionalAtMax(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	form := url.Values{}
	form.Set("name", "Test Cam")
	form.Set("webcamUrl", "http://example.com/cam.jpg")
	form.Set("latitude", "37.77")
	form.Set("longitude", "-122.42")
	form.Set("additional", "47") // maximum allowed value
	form.Set("folder", "test-cam")
	form.Set("firstSunrise", "on")
	form.Set("lastSunset", "on")

	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code == http.StatusBadRequest {
		t.Errorf("additional=47 should be valid, got 400: %s", w.Body.String())
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

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d\nbody: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// Card content
	for _, want := range []string{"Front Yard", "Active", "Render"} {
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
	form.Set("additional", "0")
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

func TestHandleNew_folderTraversal(t *testing.T) {
	srv := newTestServer(t)
	srv.router.POST("/new", srv.handleNew())

	for _, folder := range []string{"../evil", "../../etc/passwd", "..", "good/../../../evil"} {
		form := url.Values{}
		form.Set("name", "Test Cam")
		form.Set("webcamUrl", "http://example.com/cam.jpg")
		form.Set("latitude", "37.77")
		form.Set("longitude", "-122.42")
		form.Set("additional", "0")
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
