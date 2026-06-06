// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

//go:build integration

package main

// browser_test.go tests dynamic JS behaviour on the Add Webcam page using
// chromedp against Microsoft Edge (pre-installed on Windows).
//
// Run with:  go test -tags integration -run TestBrowser ./...

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
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
