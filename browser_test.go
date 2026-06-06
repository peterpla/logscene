// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

//go:build integration

package main

// browser_test.go tests dynamic JS behaviour on the Add Webcam page using
// chromedp against Microsoft Edge (pre-installed on Windows).
//
// Run with:  go test -tags integration -run TestBrowser ./...

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// edgePaths lists the two standard Edge installation locations on Windows.
var edgePaths = []string{
	`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
	`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
}

func findEdge(t *testing.T) string {
	t.Helper()
	for _, p := range edgePaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("Microsoft Edge not found — skipping browser tests")
	return ""
}

// newEdgeCtx creates a headless Edge allocator context.
// The returned cancel must be deferred by the caller.
func newEdgeCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	edge := findEdge(t)
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(edge),
		chromedp.Flag("headless", true),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	return ctx, func() { cancel(); allocCancel() }
}

// newBrowserTestServer creates a test HTTP server that serves /new and /static/*.
func newBrowserTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := newTestServer(t)
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}

	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		t.Fatalf("fs.Sub staticFS: %v", err)
	}
	srv.router.Handler("GET", "/static/*filepath",
		http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	srv.router.GET("/new", srv.handleGetNew())
	srv.router.POST("/probe", srv.handleProbe())
	srv.router.GET("/devices", srv.handleDevices())

	ts := httptest.NewServer(srv.router)
	t.Cleanup(ts.Close)
	return ts
}

// isVisible evaluates whether an element with the given id is visible
// (display != "none") in the page.
func isVisible(ctx context.Context, id string) (bool, error) {
	var display string
	err := chromedp.Run(ctx, chromedp.Evaluate(
		`getComputedStyle(document.getElementById('`+id+`')).display`,
		&display,
	))
	return display != "none" && display != "", err
}

// ---------------------------------------------------------------------------
// Source type radio buttons
// ---------------------------------------------------------------------------

func TestBrowser_defaultState_remoteCamera(t *testing.T) {
	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newBrowserTestServer(t)

	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/new"),
		chromedp.WaitVisible(`#urlSection`, chromedp.ByID),
	); err != nil {
		t.Fatal(err)
	}

	cases := []struct{ id string; wantVisible bool }{
		{"urlSection",  true},
		{"usbSection",  false},
		{"hintRemote",  true},
		{"hintUSB",     false},
		{"hintIP",      false},
		{"testShotSection", true},
	}
	for _, tc := range cases {
		got, err := isVisible(ctx, tc.id)
		if err != nil {
			t.Errorf("#%s: eval error: %v", tc.id, err)
			continue
		}
		if got != tc.wantVisible {
			t.Errorf("#%s: visible=%v, want %v", tc.id, got, tc.wantVisible)
		}
	}
}

func TestBrowser_clickUSBWebcam(t *testing.T) {
	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newBrowserTestServer(t)

	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/new"),
		chromedp.WaitVisible(`#urlSection`, chromedp.ByID),
		chromedp.Click(`#stUSB`, chromedp.ByID),
		chromedp.WaitVisible(`#usbSection`, chromedp.ByID),
	); err != nil {
		t.Fatal(err)
	}

	cases := []struct{ id string; wantVisible bool }{
		{"urlSection", false},
		{"usbSection", true},
		{"hintRemote", false},
		{"hintUSB",    true},
		{"hintIP",     false},
		{"testShotSection", false},
	}
	for _, tc := range cases {
		got, err := isVisible(ctx, tc.id)
		if err != nil {
			t.Errorf("#%s: eval error: %v", tc.id, err)
			continue
		}
		if got != tc.wantVisible {
			t.Errorf("#%s: visible=%v, want %v", tc.id, got, tc.wantVisible)
		}
	}
}

func TestBrowser_clickIPCamera(t *testing.T) {
	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newBrowserTestServer(t)

	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/new"),
		chromedp.WaitVisible(`#urlSection`, chromedp.ByID),
		chromedp.Click(`#stIP`, chromedp.ByID),
		// URL section stays visible for IP Camera; wait for hint to switch
		chromedp.WaitVisible(`#hintIP`, chromedp.ByID),
	); err != nil {
		t.Fatal(err)
	}

	cases := []struct{ id string; wantVisible bool }{
		{"urlSection", true},
		{"usbSection", false},
		{"hintRemote", false},
		{"hintUSB",    false},
		{"hintIP",     true},
		{"testShotSection", false},
	}
	for _, tc := range cases {
		got, err := isVisible(ctx, tc.id)
		if err != nil {
			t.Errorf("#%s: eval error: %v", tc.id, err)
			continue
		}
		if got != tc.wantVisible {
			t.Errorf("#%s: visible=%v, want %v", tc.id, got, tc.wantVisible)
		}
	}

	// Label should change to "Stream URL..."
	var label string
	if err := chromedp.Run(ctx,
		chromedp.Text(`#urlLabel`, &label, chromedp.ByID),
	); err != nil {
		t.Fatalf("get urlLabel text: %v", err)
	}
	if label != "Stream URL (rtsp:// or http://)" {
		t.Errorf("urlLabel: want %q, got %q", "Stream URL (rtsp:// or http://)", label)
	}
}

// ---------------------------------------------------------------------------
// Tooltip initialisation
// ---------------------------------------------------------------------------

func TestBrowser_tooltipsInitialized(t *testing.T) {
	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newBrowserTestServer(t)

	// Wait for DOMContentLoaded to fire (tooltip init happens there).
	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/new"),
		chromedp.WaitVisible(`#urlSection`, chromedp.ByID),
		// Give DOMContentLoaded listeners a moment to run.
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatal(err)
	}

	var initialized bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`bootstrap.Tooltip.getInstance(document.querySelector('[data-bs-toggle="tooltip"]')) !== null`,
		&initialized,
	)); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !initialized {
		t.Error("Bootstrap tooltip not initialized on first [data-bs-toggle=tooltip] element")
	}
}

// newDashboardTestServer creates a test HTTP server that serves / and /render,
// with one webcam that has captures today (enabling the Render button).
func newDashboardTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := newTestServer(t)
	srv.renderer = &mockRenderer{}
	if err := srv.initTemplates(); err != nil {
		t.Fatalf("initTemplates: %v", err)
	}

	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		t.Fatalf("fs.Sub staticFS: %v", err)
	}
	srv.router.Handler("GET", "/static/*filepath",
		http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	srv.router.GET("/", srv.handleHome())
	srv.router.POST("/render", srv.handleRender())

	wc := newWebcam()
	wc.Name = "Test Cam"
	wc.Folder = "test-cam"
	wc.IntervalMinutes = 15
	wc.DayFirst = time.Now().Add(-2 * time.Hour)
	wc.DayLast = time.Now().Add(6 * time.Hour)
	wc.NextCaptureAt = time.Now().Add(time.Hour)
	srv.mu.Lock()
	srv.webcams.Append(wc)
	srv.mu.Unlock()

	ts := httptest.NewServer(srv.router)
	t.Cleanup(ts.Close)
	return ts
}

// ---------------------------------------------------------------------------
// Stats initialisation
// ---------------------------------------------------------------------------

func TestBrowser_statsInitialized(t *testing.T) {
	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newBrowserTestServer(t)

	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/new"),
		chromedp.WaitVisible(`#statCaptures`, chromedp.ByID),
	); err != nil {
		t.Fatal(err)
	}

	var captures string
	if err := chromedp.Run(ctx,
		chromedp.Text(`#statCaptures`, &captures, chromedp.ByID),
	); err != nil {
		t.Fatalf("get statCaptures text: %v", err)
	}
	if captures == "" || captures == "0" {
		t.Errorf("statCaptures should be non-zero after init, got %q", captures)
	}
}

// ---------------------------------------------------------------------------
// new_webcam.html — interval preset changes stats
// ---------------------------------------------------------------------------

func TestBrowser_intervalPreset_updatesStats(t *testing.T) {
	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newBrowserTestServer(t)

	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/new"),
		chromedp.WaitVisible(`#statCaptures`, chromedp.ByID),
	); err != nil {
		t.Fatal(err)
	}

	var before string
	if err := chromedp.Run(ctx,
		chromedp.Text(`#statCaptures`, &before, chromedp.ByID),
	); err != nil {
		t.Fatalf("read initial statCaptures: %v", err)
	}

	// Click the 5-min preset; stats should update synchronously.
	var after string
	if err := chromedp.Run(ctx,
		chromedp.Click(`#int5`, chromedp.ByID),
		chromedp.Text(`#statCaptures`, &after, chromedp.ByID),
	); err != nil {
		t.Fatal(err)
	}

	if after == before {
		t.Errorf("statCaptures unchanged after switching to 5-min preset: %q", after)
	}
	// 5-min interval → floor(840/5)+2 = 170
	if after != "170" {
		t.Errorf("statCaptures for 5-min preset: want 170, got %q", after)
	}
}

// ---------------------------------------------------------------------------
// new_webcam.html — custom interval reveals section and updates stats
// ---------------------------------------------------------------------------

func TestBrowser_customInterval_showsAndUpdates(t *testing.T) {
	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newBrowserTestServer(t)

	// Bootstrap btn-check inputs have pointer-events:none in CSS; use Evaluate
	// to check the radio and dispatch the change event reliably.
	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/new"),
		chromedp.WaitVisible(`#statCaptures`, chromedp.ByID),
		chromedp.Evaluate(`(function(){
			var r = document.getElementById('intCustom');
			r.checked = true;
			r.dispatchEvent(new Event('change', {bubbles:true}));
		})()`, nil),
		chromedp.Poll(
			`document.getElementById('customIntervalSection').style.display !== 'none'`,
			nil,
			chromedp.WithPollingTimeout(2*time.Second),
		),
	); err != nil {
		t.Fatal(err)
	}

	// Set custom value to 45 min and fire input event.
	var captures string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(function(){
			var el = document.getElementById('customValue');
			el.value = '45';
			el.dispatchEvent(new Event('input', {bubbles:true}));
		})()`, nil),
		chromedp.Sleep(50*time.Millisecond),
		chromedp.Text(`#statCaptures`, &captures, chromedp.ByID),
	); err != nil {
		t.Fatal(err)
	}

	// 45-min interval → floor(840/45)+2 = 20
	if captures != "20" {
		t.Errorf("statCaptures for 45-min custom interval: want 20, got %q", captures)
	}
}

// ---------------------------------------------------------------------------
// new_webcam.html — load devices updates the USB select
// ---------------------------------------------------------------------------

func TestBrowser_loadDevices_updatesSelect(t *testing.T) {
	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newBrowserTestServer(t)

	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/new"),
		chromedp.WaitVisible(`#urlSection`, chromedp.ByID),
		// Clicking USB triggers applySourceType('usb') → loadDevices().
		chromedp.Click(`#stUSB`, chromedp.ByID),
		chromedp.WaitVisible(`#usbSection`, chromedp.ByID),
	); err != nil {
		t.Fatal(err)
	}

	// Wait for the fetch to complete: option text should no longer say "Scanning…"
	if err := chromedp.Run(ctx,
		chromedp.Poll(
			`!document.getElementById('deviceName').options[0].text.startsWith('Scanning')`,
			nil,
			chromedp.WithPollingTimeout(3*time.Second),
		),
	); err != nil {
		t.Fatalf("deviceName never updated from 'Scanning': %v", err)
	}

	var optText string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.getElementById('deviceName').options[0].text`, &optText),
	); err != nil {
		t.Fatalf("read deviceName option: %v", err)
	}
	if strings.Contains(optText, "Loading") || strings.Contains(optText, "Scanning") {
		t.Errorf("deviceName still shows loading state: %q", optText)
	}
}

// ---------------------------------------------------------------------------
// new_webcam.html — folder blur normalizes the value
// ---------------------------------------------------------------------------

func TestBrowser_folderBlur_normalizes(t *testing.T) {
	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newBrowserTestServer(t)

	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/new"),
		chromedp.WaitVisible(`#folder`, chromedp.ByID),
		chromedp.Click(`#folder`, chromedp.ByID),
		chromedp.SendKeys(`#folder`, "My Camera!!", chromedp.ByID),
		chromedp.Evaluate(`document.getElementById('folder').dispatchEvent(new Event('blur'))`, nil),
		chromedp.Sleep(50*time.Millisecond),
	); err != nil {
		t.Fatal(err)
	}

	var val, hint string
	if err := chromedp.Run(ctx,
		chromedp.Value(`#folder`, &val, chromedp.ByID),
		chromedp.Text(`#folderHint`, &hint, chromedp.ByID),
	); err != nil {
		t.Fatalf("read folder/hint: %v", err)
	}
	if val != "my-camera" {
		t.Errorf("folder value after blur: want %q, got %q", "my-camera", val)
	}
	if !strings.Contains(hint, "my-camera") {
		t.Errorf("folderHint should mention normalized value, got %q", hint)
	}
}

// ---------------------------------------------------------------------------
// new_webcam.html — first/last fixed-time toggle reveals time input
// ---------------------------------------------------------------------------

func TestBrowser_firstOption_fixedTime(t *testing.T) {
	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newBrowserTestServer(t)

	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/new"),
		chromedp.WaitVisible(`#urlSection`, chromedp.ByID),
		chromedp.Click(`#firstTime`, chromedp.ByID),
		chromedp.WaitVisible(`#firstTimeSection`, chromedp.ByID),
	); err != nil {
		t.Fatal(err)
	}

	visible, err := isVisible(ctx, "firstTimeSection")
	if err != nil {
		t.Fatalf("isVisible: %v", err)
	}
	if !visible {
		t.Error("#firstTimeSection not visible after clicking Fixed Time")
	}
}

func TestBrowser_lastOption_fixedTime(t *testing.T) {
	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newBrowserTestServer(t)

	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/new"),
		chromedp.WaitVisible(`#urlSection`, chromedp.ByID),
		chromedp.Click(`#lastTime`, chromedp.ByID),
		chromedp.WaitVisible(`#lastTimeSection`, chromedp.ByID),
	); err != nil {
		t.Fatal(err)
	}

	visible, err := isVisible(ctx, "lastTimeSection")
	if err != nil {
		t.Fatalf("isVisible: %v", err)
	}
	if !visible {
		t.Error("#lastTimeSection not visible after clicking Fixed Time")
	}
}

// ---------------------------------------------------------------------------
// new_webcam.html — test shot
// ---------------------------------------------------------------------------

func TestBrowser_testShot_noURL(t *testing.T) {
	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newBrowserTestServer(t)

	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/new"),
		chromedp.WaitVisible(`#urlSection`, chromedp.ByID),
		// Make testShotSection visible (it starts hidden; applySourceType shows it).
		chromedp.Evaluate(`document.getElementById('testShotSection').style.display = ''`, nil),
		chromedp.WaitVisible(`#testShotBtn`, chromedp.ByID),
		chromedp.Click(`#testShotBtn`, chromedp.ByID),
		chromedp.Sleep(100*time.Millisecond),
	); err != nil {
		t.Fatal(err)
	}

	var result string
	if err := chromedp.Run(ctx,
		chromedp.Text(`#testShotResult`, &result, chromedp.ByID),
	); err != nil {
		t.Fatalf("read testShotResult: %v", err)
	}
	if !strings.Contains(result, "URL") && !strings.Contains(result, "url") {
		t.Errorf("testShotResult for empty URL: want mention of URL, got %q", result)
	}
}

func TestBrowser_testShot_success(t *testing.T) {
	// Fake camera server returns a small JPEG-like payload.
	camSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		fmt.Fprint(w, "fake-image-data-1234567890") // ~26 bytes → 0 KB rounds to 0, use enough bytes
		for i := 0; i < 100; i++ {
			fmt.Fprint(w, "padding-to-make-it-bigger-")
		}
	}))
	defer camSrv.Close()

	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newBrowserTestServer(t)

	camURL := camSrv.URL + "/cam.jpg"

	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/new"),
		chromedp.WaitVisible(`#urlSection`, chromedp.ByID),
		chromedp.SetValue(`#webcamUrl`, camURL, chromedp.ByID),
		chromedp.Evaluate(`document.getElementById('testShotSection').style.display = ''`, nil),
		chromedp.WaitVisible(`#testShotBtn`, chromedp.ByID),
		chromedp.Click(`#testShotBtn`, chromedp.ByID),
		chromedp.Poll(
			`document.getElementById('testShotResult').textContent.includes('KB')`,
			nil,
			chromedp.WithPollingTimeout(5*time.Second),
		),
	); err != nil {
		t.Fatal(err)
	}

	var result string
	if err := chromedp.Run(ctx,
		chromedp.Text(`#testShotResult`, &result, chromedp.ByID),
	); err != nil {
		t.Fatalf("read testShotResult: %v", err)
	}
	if !strings.Contains(result, "KB") {
		t.Errorf("testShotResult after success: want KB size, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// dashboard.html — render modal
// ---------------------------------------------------------------------------

func TestBrowser_renderModal_opensWithStride(t *testing.T) {
	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newDashboardTestServer(t)

	var optCount int
	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/"),
		chromedp.WaitVisible(`button[data-bs-toggle="modal"]`, chromedp.ByQuery),
		chromedp.Click(`button[data-bs-toggle="modal"]`, chromedp.ByQuery),
		chromedp.Poll(
			`document.getElementById('renderStride').options.length > 1`,
			nil,
			chromedp.WithPollingTimeout(3*time.Second),
		),
		chromedp.Evaluate(`document.getElementById('renderStride').options.length`, &optCount),
	); err != nil {
		t.Fatal(err)
	}
	// 15-min interval generates options: every capture, every 2nd (30m), every 4th (60m), every 8th (120m)
	if optCount <= 1 {
		t.Errorf("stride dropdown: want >1 options for 15-min interval, got %d", optCount)
	}
}

func TestBrowser_renderModal_strideChange_updatesEstimate(t *testing.T) {
	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newDashboardTestServer(t)

	// Run 1: open modal, wait for estimate to be populated.
	var before string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/"),
		chromedp.WaitVisible(`button[data-bs-toggle="modal"]`, chromedp.ByQuery),
		chromedp.Click(`button[data-bs-toggle="modal"]`, chromedp.ByQuery),
		chromedp.Poll(
			`document.getElementById('renderEstimate').textContent !== ''`,
			nil,
			chromedp.WithPollingTimeout(3*time.Second),
		),
		chromedp.Evaluate(`document.getElementById('renderEstimate').textContent`, &before),
	); err != nil {
		t.Fatal(err)
	}

	// Run 2: change stride and read updated estimate.
	var after string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(function(){
			var s = document.getElementById('renderStride');
			s.selectedIndex = 1;
			s.dispatchEvent(new Event('change', {bubbles:true}));
		})()`, nil),
		chromedp.Sleep(100*time.Millisecond),
		chromedp.Evaluate(`document.getElementById('renderEstimate').textContent`, &after),
	); err != nil {
		t.Fatal(err)
	}

	if before == after {
		t.Errorf("estimate unchanged after stride change: %q", after)
	}
	if after == "" {
		t.Error("estimate is empty after stride change")
	}
}

func TestBrowser_renderModal_dateRange_updatesEstimate(t *testing.T) {
	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newDashboardTestServer(t)

	// Open modal and wait for initial estimate.
	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/"),
		chromedp.WaitVisible(`button[data-bs-toggle="modal"]`, chromedp.ByQuery),
		chromedp.Click(`button[data-bs-toggle="modal"]`, chromedp.ByQuery),
		chromedp.Poll(
			`document.getElementById('renderEstimate').textContent !== ''`,
			nil,
			chromedp.WithPollingTimeout(3*time.Second),
		),
	); err != nil {
		t.Fatal(err)
	}

	// Set date range and read updated estimate.
	var estimate string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(function(){
			var s = document.getElementById('renderStart');
			s.value = '2026-06-01';
			s.dispatchEvent(new Event('change', {bubbles:true}));
			var e = document.getElementById('renderEnd');
			e.value = '2026-06-07';
			e.dispatchEvent(new Event('change', {bubbles:true}));
		})()`, nil),
		chromedp.Sleep(100*time.Millisecond),
		chromedp.Evaluate(`document.getElementById('renderEstimate').textContent`, &estimate),
	); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(estimate, "days") {
		t.Errorf("estimate with date range: want 'days', got %q", estimate)
	}
	if !strings.Contains(estimate, "frames") {
		t.Errorf("estimate with date range: want 'frames', got %q", estimate)
	}
}

func TestBrowser_renderModal_fps_updatesEstimate(t *testing.T) {
	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newDashboardTestServer(t)

	// Open modal with a date range so estimate is in "X s at Y fps" form.
	var before string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/"),
		chromedp.WaitVisible(`button[data-bs-toggle="modal"]`, chromedp.ByQuery),
		chromedp.Click(`button[data-bs-toggle="modal"]`, chromedp.ByQuery),
		chromedp.Poll(
			`document.getElementById('renderEstimate').textContent !== ''`,
			nil,
			chromedp.WithPollingTimeout(3*time.Second),
		),
		chromedp.Evaluate(`(function(){
			document.getElementById('renderStart').value = '2026-06-01';
			document.getElementById('renderStart').dispatchEvent(new Event('change',{bubbles:true}));
			document.getElementById('renderEnd').value = '2026-06-07';
			document.getElementById('renderEnd').dispatchEvent(new Event('change',{bubbles:true}));
		})()`, nil),
		chromedp.Sleep(100*time.Millisecond),
		chromedp.Evaluate(`document.getElementById('renderEstimate').textContent`, &before),
	); err != nil {
		t.Fatal(err)
	}

	// Change FPS and read updated estimate.
	var after string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(function(){
			var f = document.getElementById('renderFPS');
			f.value = '12';
			f.dispatchEvent(new Event('input', {bubbles:true}));
		})()`, nil),
		chromedp.Sleep(100*time.Millisecond),
		chromedp.Evaluate(`document.getElementById('renderEstimate').textContent`, &after),
	); err != nil {
		t.Fatal(err)
	}

	if before == after {
		t.Errorf("estimate unchanged after FPS change: before=%q after=%q", before, after)
	}
	if !strings.Contains(after, "12 fps") {
		t.Errorf("estimate after FPS=12: want '12 fps', got %q", after)
	}
}

func TestBrowser_renderModal_submit(t *testing.T) {
	ctx, cancel := newEdgeCtx(t)
	defer cancel()
	ts := newDashboardTestServer(t)

	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/"),
		chromedp.WaitVisible(`button[data-bs-toggle="modal"]`, chromedp.ByQuery),
		chromedp.Click(`button[data-bs-toggle="modal"]`, chromedp.ByQuery),
		chromedp.Poll(
			`document.getElementById('renderStride').options.length > 1`,
			nil,
			chromedp.WithPollingTimeout(3*time.Second),
		),
		chromedp.Click(`#renderSubmit`, chromedp.ByID),
		chromedp.Poll(
			`document.getElementById('renderStatus').textContent !== ''`,
			nil,
			chromedp.WithPollingTimeout(3*time.Second),
		),
	); err != nil {
		t.Fatal(err)
	}

	var status string
	if err := chromedp.Run(ctx,
		chromedp.Text(`#renderStatus`, &status, chromedp.ByID),
	); err != nil {
		t.Fatalf("read renderStatus: %v", err)
	}
	if !strings.Contains(status, "Rendering") {
		t.Errorf("renderStatus after submit: want 'Rendering', got %q", status)
	}
}
