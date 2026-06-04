// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// clients_test.go tests the three HTTP client implementations using
// httptest.NewServer to avoid real network calls.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// roundTripFunc lets a plain function satisfy http.RoundTripper so tests can
// redirect all outbound requests to a local httptest.Server.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// redirectTo returns a RoundTripper that rewrites the scheme+host of every
// request to point at the given test server URL, preserving path and query.
func redirectTo(serverURL string) http.RoundTripper {
	u, _ := url.Parse(serverURL)
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		r2 := req.Clone(req.Context())
		r2.URL.Scheme = u.Scheme
		r2.URL.Host = u.Host
		return http.DefaultTransport.RoundTrip(r2)
	})
}

// ---------------------------------------------------------------------------
// HTTPTimezoneClient
// ---------------------------------------------------------------------------

func TestHTTPTimezoneClient_emptyKey(t *testing.T) {
	c := NewHTTPTimezoneClient("")
	_, err := c.GetTimezone(context.Background(), 34.0, -118.0)
	if err == nil {
		t.Error("expected error for empty API key, got nil")
	}
}

func TestHTTPTimezoneClient_success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"OK","zoneName":"America/Los_Angeles"}`)
	}))
	defer ts.Close()

	c := &HTTPTimezoneClient{
		apiKey: "test-key",
		client: &http.Client{Transport: redirectTo(ts.URL)},
	}
	tz, err := c.GetTimezone(context.Background(), 34.0, -118.0)
	if err != nil {
		t.Fatalf("GetTimezone: %v", err)
	}
	if tz != "America/Los_Angeles" {
		t.Errorf("timezone: want %q, got %q", "America/Los_Angeles", tz)
	}
}

func TestHTTPTimezoneClient_non200(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer ts.Close()

	c := &HTTPTimezoneClient{
		apiKey: "test-key",
		client: &http.Client{Transport: redirectTo(ts.URL)},
	}
	_, err := c.GetTimezone(context.Background(), 34.0, -118.0)
	if err == nil {
		t.Error("expected error for HTTP 500, got nil")
	}
}

func TestHTTPTimezoneClient_apiError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"FAILED","message":"invalid API key"}`)
	}))
	defer ts.Close()

	c := &HTTPTimezoneClient{
		apiKey: "test-key",
		client: &http.Client{Transport: redirectTo(ts.URL)},
	}
	_, err := c.GetTimezone(context.Background(), 34.0, -118.0)
	if err == nil {
		t.Error("expected error for API status FAILED, got nil")
	}
	if !strings.Contains(err.Error(), "invalid API key") {
		t.Errorf("error should mention API message: %v", err)
	}
}

func TestHTTPTimezoneClient_rateLimitContextCancel(t *testing.T) {
	// Server always returns 429; context is cancelled quickly so the retry
	// loop exits via ctx.Done() rather than waiting the full backoff delay.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := &HTTPTimezoneClient{
		apiKey: "test-key",
		client: &http.Client{Transport: redirectTo(ts.URL)},
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := c.GetTimezone(ctx, 34.0, -118.0)
	if err == nil {
		t.Error("expected error after context cancellation, got nil")
	}
}

// ---------------------------------------------------------------------------
// HTTPSolarClient
// ---------------------------------------------------------------------------

const solarOKBody = `{
	"results": {
		"sunrise": "2026-06-01T13:00:00Z",
		"solar_noon": "2026-06-01T19:50:00Z",
		"sunset": "2026-06-02T03:00:00Z"
	},
	"status": "OK"
}`

func TestHTTPSolarClient_success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, solarOKBody)
	}))
	defer ts.Close()

	c := NewHTTPSolarClient()
	c.client = &http.Client{Transport: redirectTo(ts.URL)}
	webcamDate := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	st, err := c.GetSolarTimes(context.Background(), 34.0, -118.0, webcamDate)
	if err != nil {
		t.Fatalf("GetSolarTimes: %v", err)
	}
	wantSunrise := time.Date(2026, 6, 1, 13, 0, 0, 0, time.UTC)
	if !st.Sunrise.Equal(wantSunrise) {
		t.Errorf("Sunrise: want %v, got %v", wantSunrise, st.Sunrise)
	}
}

func TestHTTPSolarClient_plus00offset(t *testing.T) {
	// sunrise-sunset.org sometimes returns "+00:00" instead of "Z".
	body := strings.ReplaceAll(solarOKBody, `Z"`, `+00:00"`)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer ts.Close()

	c := &HTTPSolarClient{client: &http.Client{Transport: redirectTo(ts.URL)}}
	webcamDate := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	st, err := c.GetSolarTimes(context.Background(), 34.0, -118.0, webcamDate)
	if err != nil {
		t.Fatalf("GetSolarTimes with +00:00 offset: %v", err)
	}
	if st.Sunrise.IsZero() {
		t.Error("Sunrise should not be zero after +00:00 normalization")
	}
}

func TestHTTPSolarClient_parseError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Valid envelope and status, but sunrise value is not a parseable time.
		fmt.Fprint(w, `{"results":{"sunrise":"not-a-time","solar_noon":"2026-06-01T19:50:00Z","sunset":"2026-06-02T03:00:00Z"},"status":"OK"}`)
	}))
	defer ts.Close()

	c := NewHTTPSolarClient()
	c.client = &http.Client{Transport: redirectTo(ts.URL)}
	_, err := c.GetSolarTimes(context.Background(), 34.0, -118.0, time.Now())
	if err == nil {
		t.Error("expected parse error for malformed sunrise time, got nil")
	}
}

func TestHTTPSolarClient_non200(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer ts.Close()

	c := &HTTPSolarClient{client: &http.Client{Transport: redirectTo(ts.URL)}}
	_, err := c.GetSolarTimes(context.Background(), 34.0, -118.0, time.Now())
	if err == nil {
		t.Error("expected error for HTTP 500, got nil")
	}
}

func TestHTTPSolarClient_invalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "not-json-at-all")
	}))
	defer ts.Close()

	c := &HTTPSolarClient{client: &http.Client{Transport: redirectTo(ts.URL)}}
	_, err := c.GetSolarTimes(context.Background(), 34.0, -118.0, time.Now())
	if err == nil {
		t.Error("expected error for malformed JSON body, got nil")
	}
}

func TestHTTPSolarClient_apiError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":{},"status":"INVALID_REQUEST"}`)
	}))
	defer ts.Close()

	c := &HTTPSolarClient{client: &http.Client{Transport: redirectTo(ts.URL)}}
	_, err := c.GetSolarTimes(context.Background(), 34.0, -118.0, time.Now())
	if err == nil {
		t.Error("expected error for API status INVALID_REQUEST, got nil")
	}
}

// ---------------------------------------------------------------------------
// HTTPImageFetcher
// ---------------------------------------------------------------------------

func TestHTTPImageFetcher_success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("fake-jpeg-data"))
	}))
	defer ts.Close()

	fetcher := NewHTTPImageFetcher()
	body, ct, err := fetcher.Fetch(context.Background(), ts.URL+"/cam.jpg")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer body.Close()
	if ct != "image/jpeg" {
		t.Errorf("content-type: want %q, got %q", "image/jpeg", ct)
	}
	data, _ := io.ReadAll(body)
	if string(data) != "fake-jpeg-data" {
		t.Errorf("body: want %q, got %q", "fake-jpeg-data", string(data))
	}
}

func TestHTTPImageFetcher_non200(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer ts.Close()

	fetcher := NewHTTPImageFetcher()
	_, _, err := fetcher.Fetch(context.Background(), ts.URL+"/cam.jpg")
	if err == nil {
		t.Error("expected error for HTTP 404, got nil")
	}
}

// ---------------------------------------------------------------------------
// Do-failure paths (transport returns an error before a response is received)
// ---------------------------------------------------------------------------

func alwaysFailTransport() http.RoundTripper {
	return roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})
}

func TestHTTPTimezoneClient_doError(t *testing.T) {
	c := &HTTPTimezoneClient{
		apiKey: "test-key",
		client: &http.Client{Transport: alwaysFailTransport()},
	}
	_, err := c.GetTimezone(context.Background(), 34.0, -118.0)
	if err == nil {
		t.Error("expected error for transport failure, got nil")
	}
}

func TestHTTPSolarClient_doError(t *testing.T) {
	c := NewHTTPSolarClient()
	c.client = &http.Client{Transport: alwaysFailTransport()}
	_, err := c.GetSolarTimes(context.Background(), 34.0, -118.0, time.Now())
	if err == nil {
		t.Error("expected error for transport failure, got nil")
	}
}

func TestHTTPImageFetcher_doError(t *testing.T) {
	fetcher := NewHTTPImageFetcher()
	fetcher.client = &http.Client{Transport: alwaysFailTransport()}
	_, _, err := fetcher.Fetch(context.Background(), "http://example.com/cam.jpg")
	if err == nil {
		t.Error("expected error for transport failure, got nil")
	}
}
