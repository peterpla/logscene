// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// slog_messages_test.go confirms that each slog call site routes its message
// to the correct stream:
//   - slog.Info  → appears in both userLog (Info+, TextHandler) and debugLog (Debug+, JSONHandler)
//   - slog.Debug → appears only in debugLog
//
// captureLogs installs an in-memory multiHandler so tests do not touch the
// filesystem. The prior slog default is restored via t.Cleanup.

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// captureLogs installs an in-memory multiHandler as slog's default and returns
// two buffers: userLog (Info+, TextHandler) and debugLog (Debug+, JSONHandler).
func captureLogs(t *testing.T) (userLog, debugLog *bytes.Buffer) {
	t.Helper()
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })
	userLog = &bytes.Buffer{}
	debugLog = &bytes.Buffer{}
	userH := slog.NewTextHandler(userLog, &slog.HandlerOptions{Level: slog.LevelInfo})
	debugH := slog.NewJSONHandler(debugLog, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(&multiHandler{handlers: []slog.Handler{userH, debugH}}))
	return userLog, debugLog
}

// assertInDebugOnly asserts msg appears in debugLog but NOT in userLog.
func assertInDebugOnly(t *testing.T, msg string, userLog, debugLog *bytes.Buffer) {
	t.Helper()
	if strings.Contains(userLog.String(), msg) {
		t.Errorf("user log should NOT contain %q:\n%s", msg, userLog)
	}
	if !strings.Contains(debugLog.String(), msg) {
		t.Errorf("debug log missing %q:\n%s", msg, debugLog)
	}
}

// assertInBoth asserts msg appears in both userLog and debugLog.
func assertInBoth(t *testing.T, msg string, userLog, debugLog *bytes.Buffer) {
	t.Helper()
	if !strings.Contains(userLog.String(), msg) {
		t.Errorf("user log missing %q:\n%s", msg, userLog)
	}
	if !strings.Contains(debugLog.String(), msg) {
		t.Errorf("debug log missing %q:\n%s", msg, debugLog)
	}
}

// ─── config.go ───────────────────────────────────────────────────────────────

func TestSlogMsg_config_invalidPoll(t *testing.T) {
	userLog, debugLog := captureLogs(t)

	var cfg Config
	cfg.loadFrom(flag.NewFlagSet("test", flag.ContinueOnError), nil, func(k string) string {
		if k == "LOGSCENE_POLL" {
			return "not-a-number"
		}
		return ""
	})

	assertInDebugOnly(t, "invalid LOGSCENE_POLL value, using default", userLog, debugLog)
}

func TestSlogMsg_config_resolved(t *testing.T) {
	userLog, debugLog := captureLogs(t)

	var cfg Config
	cfg.loadFrom(flag.NewFlagSet("test", flag.ContinueOnError), nil, func(string) string { return "" })

	assertInDebugOnly(t, "config resolved", userLog, debugLog)
}

func TestSlogMsg_config_devMode(t *testing.T) {
	userLog, debugLog := captureLogs(t)

	var cfg Config
	cfg.loadFrom(flag.NewFlagSet("test", flag.ContinueOnError), nil, func(k string) string {
		if k == "LOGSCENE_DEV" {
			return "1"
		}
		return ""
	})

	assertInBoth(t, "dev mode enabled — trial suppressed, webcam cap lifted", userLog, debugLog)
}

// ─── main.go — buildStorage ───────────────────────────────────────────────────

func TestSlogMsg_buildStorage_gcs(t *testing.T) {
	userLog, debugLog := captureLogs(t)
	buildStorage("gcs")
	assertInDebugOnly(t, "storage backend not yet implemented, falling back to local", userLog, debugLog)
	if !strings.Contains(debugLog.String(), "gcs") {
		t.Errorf("debug log should contain requested=gcs:\n%s", debugLog)
	}
}

func TestSlogMsg_buildStorage_s3(t *testing.T) {
	userLog, debugLog := captureLogs(t)
	buildStorage("s3")
	assertInDebugOnly(t, "storage backend not yet implemented, falling back to local", userLog, debugLog)
	if !strings.Contains(debugLog.String(), "s3") {
		t.Errorf("debug log should contain requested=s3:\n%s", debugLog)
	}
}

// ─── webcam.go — Webcams.Write ────────────────────────────────────────────────

func TestSlogMsg_webcamsWrite(t *testing.T) {
	userLog, debugLog := captureLogs(t)

	dir := t.TempDir()
	if err := newWebcams().Write(dir, newValidator()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	assertInDebugOnly(t, "config written", userLog, debugLog)
	if !strings.Contains(debugLog.String(), masterFile) {
		t.Errorf("debug log should contain %q in path attr:\n%s", masterFile, debugLog)
	}
}

// ─── render.go — LocalRenderer.Render ────────────────────────────────────────

// TestSlogMsg_render_start confirms the "starting timelapse render" Info entry
// and the "render started" Debug entry appear in the correct streams.
// The render may fail (ffmpeg absent); slog fires before ffmpeg is invoked.
func TestSlogMsg_render_start(t *testing.T) {
	userLog, debugLog := captureLogs(t)

	dir := t.TempDir()
	// Any .jpg serves as a frame; timestamp suffix is only required for date filtering.
	if err := os.WriteFile(filepath.Join(dir, "cam 20260601120000.jpg"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	r := NewLocalRenderer()
	_ = r.Render(context.Background(), dir, filepath.Join(dir, "out.mp4"), RenderOptions{})

	assertInBoth(t, "starting timelapse render", userLog, debugLog)
	assertInDebugOnly(t, "render started", userLog, debugLog)
}

// TestSlogMsg_render_complete confirms the "timelapse render complete" Info entry
// and the "render complete" Debug entry. Skipped when ffmpeg is unavailable.
func TestSlogMsg_render_complete(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH")
	}

	// Generate a real 1-frame JPEG using lavfi; needed for ffmpeg to produce output.
	frameDir := t.TempDir()
	framePath := filepath.Join(frameDir, "cam 20260601120000.jpg")
	setupCmd := exec.Command("ffmpeg",
		"-f", "lavfi", "-i", "testsrc=duration=1:size=10x10",
		"-frames:v", "1", "-y", framePath)
	if out, err := setupCmd.CombinedOutput(); err != nil {
		t.Skipf("lavfi frame setup failed: %v\n%s", err, out)
	}

	userLog, debugLog := captureLogs(t)

	r := NewLocalRenderer()
	if err := r.Render(context.Background(), frameDir, filepath.Join(frameDir, "out.mp4"), RenderOptions{}); err != nil {
		t.Fatalf("Render: %v", err)
	}

	assertInBoth(t, "timelapse render complete", userLog, debugLog)
	// "render complete" is Debug-only. Use JSON-specific search to avoid a false
	// match on "timelapse render complete" (which is a super-string) in userLog.
	if strings.Contains(userLog.String(), `msg="render complete"`) {
		t.Errorf("user log should not contain Debug 'render complete' message:\n%s", userLog)
	}
	if !strings.Contains(debugLog.String(), `"msg":"render complete"`) {
		t.Errorf("debug log missing 'render complete' debug message:\n%s", debugLog)
	}
}

// ─── schedule.go — SetCaptureTimes ───────────────────────────────────────────

func TestSlogMsg_schedule_computed(t *testing.T) {
	userLog, debugLog := captureLogs(t)

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 15)
	err := wc.SetCaptureTimes(
		context.Background(), time.Now(),
		&fixedTimezoneClient{tz: "America/Los_Angeles"},
		&fixedSolarClient{times: futureSolarTimes()},
	)
	if err != nil {
		t.Fatalf("SetCaptureTimes: %v", err)
	}

	assertInDebugOnly(t, "schedule computed", userLog, debugLog)
}

func TestSlogMsg_schedule_unrecognizedCachedTZ(t *testing.T) {
	userLog, debugLog := captureLogs(t)

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 15)
	wc.mu.Lock()
	wc.WebcamTZ = "Not/A/Valid/Timezone"
	wc.mu.Unlock()

	err := wc.SetCaptureTimes(
		context.Background(), time.Now(),
		&fixedTimezoneClient{tz: "America/Los_Angeles"},
		&fixedSolarClient{times: futureSolarTimes()},
	)
	if err != nil {
		t.Fatalf("SetCaptureTimes: %v", err)
	}

	assertInDebugOnly(t, "cached timezone unrecognized, re-fetching", userLog, debugLog)
}

// ─── capture.go goroutine ─────────────────────────────────────────────────────

func TestSlogMsg_capture_disabled(t *testing.T) {
	userLog, debugLog := captureLogs(t)

	srv := newTestServer(t)
	wc := newWebcam()
	wc.Disabled = true

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv.webcamWg.Add(1)

	done := make(chan struct{})
	go func() {
		capture(ctx, wc, time.Millisecond, srv)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("capture goroutine did not exit for disabled webcam")
	}

	assertInBoth(t, "webcam disabled — no captures", userLog, debugLog)
	assertInDebugOnly(t, "webcam disabled at startup, goroutine exiting", userLog, debugLog)
}

func TestSlogMsg_capture_setCaptureTimesFailed(t *testing.T) {
	userLog, debugLog := captureLogs(t)

	srv := newTestServer(t)
	srv.tz = &fixedTimezoneClient{err: errors.New("timezone service unavailable")}

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 15)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv.webcamWg.Add(1)

	done := make(chan struct{})
	go func() {
		capture(ctx, wc, time.Millisecond, srv)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("capture goroutine did not exit after SetCaptureTimes failure")
	}

	assertInDebugOnly(t, "SetCaptureTimes failed", userLog, debugLog)
}

func TestSlogMsg_capture_malformedTimezone(t *testing.T) {
	userLog, debugLog := captureLogs(t)

	srv := newTestServer(t)
	srv.tz = &fixedTimezoneClient{tz: "Not/A/Real/Timezone"}

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 15)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv.webcamWg.Add(1)

	done := make(chan struct{})
	go func() {
		capture(ctx, wc, time.Millisecond, srv)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("capture goroutine did not exit after malformed timezone")
	}

	assertInBoth(t, "webcam timezone is invalid", userLog, debugLog)
	assertInDebugOnly(t, "SetCaptureTimes: malformed timezone, goroutine exiting", userLog, debugLog)

	if got, _ := srv.status.Get(wc.Name); got != StatusError {
		t.Errorf("status = %v, want StatusError", got)
	}
}

func TestSlogMsg_capture_endAndStartOfDay(t *testing.T) {
	userLog, debugLog := captureLogs(t)

	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 60)
	wc.mu.Lock()
	wc.WebcamTZ = "America/Los_Angeles"
	wc.WebcamLoc = loc
	wc.DayLast = time.Now().Add(-5 * time.Minute)
	wc.DayFirst = wc.DayLast.Add(-8 * time.Hour)
	wc.NextCaptureAt = wc.DayLast
	wc.CaptureCountToday = 7
	wc.ScheduledCountToday = 9
	wc.mu.Unlock()

	tzClient := &fixedTimezoneClient{tz: "America/Los_Angeles"}
	solar := &fixedSolarClient{times: futureSolarTimes()}

	if err := wc.UpdateNextCapture(context.Background(), tzClient, solar); err != nil {
		t.Fatalf("UpdateNextCapture: %v", err)
	}

	assertInBoth(t, "end of day", userLog, debugLog)
	assertInBoth(t, "start of day", userLog, debugLog)
}

func TestSlogMsg_capture_autoSuspend(t *testing.T) {
	userLog, debugLog := captureLogs(t)

	srv := newTestServer(t)
	srv.solar = &fixedSolarClient{times: futureSolarTimes()}

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 15)
	wc.FirstFailure = time.Now().Add(-15 * 24 * time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv.webcamWg.Add(1)

	done := make(chan struct{})
	go func() {
		capture(ctx, wc, time.Millisecond, srv)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("capture goroutine did not exit via auto-suspend")
	}

	assertInBoth(t, "webcam could not be reached for 2 consecutive weeks", userLog, debugLog)
	assertInDebugOnly(t, "auto-suspend threshold reached", userLog, debugLog)
}

// ─── handlers.go — handleRender failure ──────────────────────────────────────

func TestSlogMsg_handleRender_failure(t *testing.T) {
	srv := newTestServer(t)
	mr := &mockRenderer{
		renderErr: errors.New("intentional render failure"),
		called:    make(chan struct{}),
	}
	srv.renderer = mr
	srv.router.POST("/render", srv.handleRender())

	// Install after setup to avoid capturing any incidental slog output from
	// router registration (there is none currently, but this is defensive).
	userLog, debugLog := captureLogs(t)

	body := `{"folder":"test-cam","output":"out.mp4"}`
	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.router.ServeHTTP(httptest.NewRecorder(), req)

	// mr.called closes when Render() is invoked; the slog lines fire
	// synchronously in the same goroutine immediately after Render returns.
	select {
	case <-mr.called:
	case <-time.After(2 * time.Second):
		t.Fatal("render goroutine did not invoke Render within 2s")
	}
	// Poll briefly for slog output that fires after Render returns.
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) && !strings.Contains(debugLog.String(), "handleRender: render failed") {
		time.Sleep(time.Millisecond)
	}

	assertInBoth(t, "render failed", userLog, debugLog)
	assertInDebugOnly(t, "handleRender: render failed", userLog, debugLog)
}

// ─── handlers.go — handleReload ──────────────────────────────────────────────

func TestSlogMsg_handleReload_complete(t *testing.T) {
	srv := newTestServer(t)
	writeEmptyConfig(t, srv)
	srv.router.POST("/reload", srv.handleReload())

	// Install after writeEmptyConfig so its "config written" debug noise is not captured.
	userLog, debugLog := captureLogs(t)

	req := httptest.NewRequest(http.MethodPost, "/reload", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("reload: want 200, got %d: %s", w.Code, w.Body.String())
	}
	assertInDebugOnly(t, "handleReload: complete", userLog, debugLog)
}
