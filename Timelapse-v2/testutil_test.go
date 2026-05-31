package main

// testutil_test.go provides mock implementations of all injected interfaces
// and a testStorage() helper that selects the storage backend via TEST_STORAGE.
//
// Mocks use simple function fields ("stubs") rather than a mock library so
// each test can supply exactly the behaviour it needs in a few lines.

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// testStorage — selects backend via TEST_STORAGE env var
// ---------------------------------------------------------------------------

// testStorage returns the Storage backend requested by the TEST_STORAGE
// environment variable:
//
//	unset / "memory"  → MemStorage (default, no filesystem access)
//	"local"           → LocalStorage pointed at a t.TempDir()
//	"gcs" / "s3"      → skipped until credentials are configured
func testStorage(t *testing.T) Storage {
	t.Helper()
	switch backend := os.Getenv("TEST_STORAGE"); backend {
	case "local":
		_ = t.TempDir() // ensure a temp dir exists; keys must be absolute paths in tests
		return NewLocalStorage()
	case "gcs":
		t.Skip("GCS storage not yet implemented — set TEST_STORAGE=memory or local")
	case "s3":
		t.Skip("S3 storage not yet implemented — set TEST_STORAGE=memory or local")
	}
	return NewMemStorage()
}

// ---------------------------------------------------------------------------
// fixedTimezoneClient
// ---------------------------------------------------------------------------

// fixedTimezoneClient always returns a fixed timezone name (or error).
type fixedTimezoneClient struct {
	tz  string
	err error
}

func (f *fixedTimezoneClient) GetTimezone(_ context.Context, _, _ float64) (string, error) {
	return f.tz, f.err
}

// ---------------------------------------------------------------------------
// fixedSolarClient
// ---------------------------------------------------------------------------

// fixedSolarClient returns predetermined solar times (or error).
// capturedDate records the webcamDate argument of the most recent call,
// letting tests verify that the correct calendar date was used.
type fixedSolarClient struct {
	times        SolarTimes
	err          error
	capturedDate time.Time
}

func (f *fixedSolarClient) GetSolarTimes(_ context.Context, _, _ float64, webcamDate time.Time) (SolarTimes, error) {
	f.capturedDate = webcamDate
	return f.times, f.err
}

// ---------------------------------------------------------------------------
// mockImageFetcher
// ---------------------------------------------------------------------------

// mockImageFetcher returns fixed image data (or error) for any URL.
type mockImageFetcher struct {
	data        []byte
	contentType string
	err         error
	// callCount lets tests verify how many fetches were attempted.
	callCount int
}

func (m *mockImageFetcher) Fetch(_ context.Context, _ string) (io.ReadCloser, string, error) {
	m.callCount++
	if m.err != nil {
		return nil, "", m.err
	}
	return io.NopCloser(bytes.NewReader(m.data)), m.contentType, nil
}

// ---------------------------------------------------------------------------
// helpers for building test Webcams
// ---------------------------------------------------------------------------

// testWebcam returns a Webcam with flags already set for the given first/last options.
func testWebcam(t *testing.T, firstFlag, lastFlag uint, additional int) *Webcam {
	t.Helper()
	wc := newWebcam()
	wc.Name = "test-cam"
	wc.URL = "http://example.com/cam.jpg"
	wc.Latitude = 37.77
	wc.Longitude = -122.42
	wc.Additional = additional
	wc.Folder = "test-cam"

	wc.FirstFlags = firstFlag
	wc.LastFlags = lastFlag

	// Sync boolean fields with flags so SetFirstLastFlags won't be needed.
	wc.FirstSunrise = firstFlag&flagFirstSunrise != 0
	wc.FirstSunrise30 = firstFlag&flagFirstSunrise30 != 0
	wc.FirstSunrise60 = firstFlag&flagFirstSunrise60 != 0
	wc.FirstTime = firstFlag&flagFirstTime != 0
	wc.LastSunset = lastFlag&flagLastSunset != 0
	wc.LastSunset30 = lastFlag&flagLastSunset30 != 0
	wc.LastSunset60 = lastFlag&flagLastSunset60 != 0
	wc.LastTime = lastFlag&flagLastTime != 0

	return wc
}

// laFixedSolar returns solar times for a summer day at roughly LA latitude.
// Sunrise 06:00, solar noon 12:50, sunset 20:00 — all expressed as UTC
// (LA is UTC-7 in summer, so these are local times + 7 h).
func laFixedSolar() SolarTimes {
	// 2026-06-01 in LA (PDT = UTC-7)
	sunrise := time.Date(2026, 6, 1, 13, 0, 0, 0, time.UTC)  // 06:00 PDT
	noon := time.Date(2026, 6, 1, 19, 50, 0, 0, time.UTC)    // 12:50 PDT
	sunset := time.Date(2026, 6, 2, 3, 0, 0, 0, time.UTC)    // 20:00 PDT
	return SolarTimes{Sunrise: sunrise, SolarNoon: noon, Sunset: sunset}
}
