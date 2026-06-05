// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// schedule_test.go tests the scheduling math in schedule.go.
//
// All tests inject fixedTimezoneClient and fixedSolarClient so no network
// calls are made. The key correctness tests are:
//
//  1. buildSchedule produces the right number and ordering of times
//  2. parseTimeOfDay correctly interprets HH:MM in the webcam's timezone
//  3. SetCaptureTimes uses the webcam's calendar date (not the server's)

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// buildSchedule
// ---------------------------------------------------------------------------

func TestBuildSchedule_anchors(t *testing.T) {
	solar := laFixedSolar()
	schedule := buildSchedule(solar.Sunrise, solar.Sunset, 60)
	if len(schedule) < 2 {
		t.Fatalf("want at least 2 captures, got %d", len(schedule))
	}
	if !schedule[0].Equal(solar.Sunrise) {
		t.Errorf("first: want %v, got %v", solar.Sunrise, schedule[0])
	}
	if !schedule[len(schedule)-1].Equal(solar.Sunset) {
		t.Errorf("last: want %v, got %v", solar.Sunset, schedule[len(schedule)-1])
	}
}

func TestBuildSchedule_intervalSpacing(t *testing.T) {
	// 60-minute interval over a 4-hour span → first, +60, +120, +180, last(+240)
	first := time.Unix(0, 0)
	last := first.Add(4 * time.Hour)
	schedule := buildSchedule(first, last, 60)
	if len(schedule) != 5 {
		t.Fatalf("want 5 captures, got %d: %v", len(schedule), schedule)
	}
	for i := 1; i < len(schedule)-1; i++ {
		gap := schedule[i].Sub(schedule[i-1])
		if gap != time.Hour {
			t.Errorf("gap[%d]=%v, want 1h", i, gap)
		}
	}
	assertSorted(t, schedule)
}

func TestBuildSchedule_unevenSpan(t *testing.T) {
	// 60-minute interval over a 90-minute span → first, +60, last(+90)
	first := time.Unix(0, 0)
	last := first.Add(90 * time.Minute)
	schedule := buildSchedule(first, last, 60)
	if len(schedule) != 3 {
		t.Fatalf("want 3 captures, got %d: %v", len(schedule), schedule)
	}
	if !schedule[0].Equal(first) || !schedule[2].Equal(last) {
		t.Errorf("anchors wrong: %v", schedule)
	}
}

func TestBuildSchedule_largeInterval(t *testing.T) {
	// Interval larger than span → just first and last
	first := time.Unix(0, 0)
	last := first.Add(30 * time.Minute)
	schedule := buildSchedule(first, last, 60)
	if len(schedule) != 2 {
		t.Fatalf("want 2 captures, got %d: %v", len(schedule), schedule)
	}
}

// ---------------------------------------------------------------------------
// parseTimeOfDay
// ---------------------------------------------------------------------------

func TestParseTimeOfDay_valid(t *testing.T) {
	loc, _ := time.LoadLocation("America/Los_Angeles")
	webcamNow := time.Date(2026, 6, 1, 12, 0, 0, 0, loc)

	got, err := parseTimeOfDay("08:30", webcamNow, loc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 08:30 PDT = 15:30 UTC on 2026-06-01
	want := time.Date(2026, 6, 1, 15, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

func TestParseTimeOfDay_invalid(t *testing.T) {
	loc, _ := time.LoadLocation("America/Los_Angeles")
	webcamNow := time.Date(2026, 6, 1, 12, 0, 0, 0, loc)
	cases := []string{"", "25:00", "08:60", "notaTime", "8"}
	for _, c := range cases {
		if _, err := parseTimeOfDay(c, webcamNow, loc); err == nil {
			t.Errorf("%q: expected error, got nil", c)
		}
	}
}

// ---------------------------------------------------------------------------
// SetCaptureTimes — timezone correctness (the cloud-server bug fix)
// ---------------------------------------------------------------------------

// TestSetCaptureTimes_usesWebcamDate verifies that SetCaptureTimes extracts the
// calendar date in the *webcam's* timezone, not the server's.
//
// Setup: server time is 2026-06-02 01:00 UTC+13 (= 2026-06-01 12:00 UTC).
// Webcam is at UTC-7 (America/Los_Angeles): still 2026-06-01 05:00.
// The solar query must use "2026-06-01", not "2026-06-02".
func TestSetCaptureTimes_usesWebcamDate(t *testing.T) {
	tzClient := &fixedTimezoneClient{tz: "America/Los_Angeles"}
	solar := &fixedSolarClient{times: laFixedSolar()}

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 15)

	serverTZ := time.FixedZone("UTC+13", 13*3600)
	serverTime := time.Date(2026, 6, 2, 1, 0, 0, 0, serverTZ)

	if err := wc.SetCaptureTimes(context.Background(), serverTime, tzClient, solar); err != nil {
		t.Fatalf("SetCaptureTimes: %v", err)
	}

	y, m, d := solar.capturedDate.Date()
	if y != 2026 || m != time.June || d != 1 {
		t.Errorf("solar query date: want 2026-06-01, got %04d-%02d-%02d", y, int(m), d)
	}
}

func TestSetCaptureTimes_firstSunrise30(t *testing.T) {
	tzClient := &fixedTimezoneClient{tz: "America/Los_Angeles"}
	s := laFixedSolar()
	solar := &fixedSolarClient{times: s}
	wc := testWebcam(t, flagFirstSunrise30, flagLastSunset, 15)
	ref := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	if err := wc.SetCaptureTimes(context.Background(), ref, tzClient, solar); err != nil {
		t.Fatalf("SetCaptureTimes: %v", err)
	}
	want := s.Sunrise.Add(30 * time.Minute)
	wc.mu.RLock()
	got := wc.CaptureTimes[0]
	wc.mu.RUnlock()
	if !got.Equal(want) {
		t.Errorf("first capture: want %v, got %v", want, got)
	}
}

func TestSetCaptureTimes_lastSunset60(t *testing.T) {
	tzClient := &fixedTimezoneClient{tz: "America/Los_Angeles"}
	s := laFixedSolar()
	solar := &fixedSolarClient{times: s}
	wc := testWebcam(t, flagFirstSunrise, flagLastSunset60, 15)
	ref := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	if err := wc.SetCaptureTimes(context.Background(), ref, tzClient, solar); err != nil {
		t.Fatalf("SetCaptureTimes: %v", err)
	}
	want := s.Sunset.Add(-60 * time.Minute)
	wc.mu.RLock()
	last := wc.CaptureTimes[len(wc.CaptureTimes)-1]
	wc.mu.RUnlock()
	if !last.Equal(want) {
		t.Errorf("last capture: want %v, got %v", want, last)
	}
}

func TestSetCaptureTimes_firstTime(t *testing.T) {
	tzClient := &fixedTimezoneClient{tz: "America/Los_Angeles"}
	s := laFixedSolar()
	solar := &fixedSolarClient{times: s}

	wc := testWebcam(t, flagFirstTime, flagLastSunset, 15)
	wc.FirstTimeValue = "07:00"
	ref := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	if err := wc.SetCaptureTimes(context.Background(), ref, tzClient, solar); err != nil {
		t.Fatalf("SetCaptureTimes: %v", err)
	}
	// 07:00 PDT (UTC-7) = 14:00 UTC on 2026-06-01
	want := time.Date(2026, 6, 1, 14, 0, 0, 0, time.UTC)
	wc.mu.RLock()
	got := wc.CaptureTimes[0]
	wc.mu.RUnlock()
	if !got.Equal(want) {
		t.Errorf("first capture: want %v, got %v", want, got)
	}
}

func TestSetCaptureTimes_errorOnFutureTimesRemaining(t *testing.T) {
	tzClient := &fixedTimezoneClient{tz: "America/Los_Angeles"}
	solar := &fixedSolarClient{times: laFixedSolar()}
	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 15)

	wc.mu.Lock()
	wc.CaptureTimes = []time.Time{time.Now().Add(time.Hour)}
	wc.mu.Unlock()

	err := wc.SetCaptureTimes(context.Background(), time.Now(), tzClient, solar)
	if err == nil {
		t.Error("expected error when future times remain, got nil")
	}
}

// TestSetCaptureTimes_usesCachedTimezone verifies that when WebcamTZ is already
// populated, the timezone client is never called.
func TestSetCaptureTimes_usesCachedTimezone(t *testing.T) {
	// Client returns an error — if it were called, SetCaptureTimes would fail.
	tzClient := &fixedTimezoneClient{err: fmt.Errorf("should not be called")}
	solar := &fixedSolarClient{times: laFixedSolar()}

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 15)
	wc.WebcamTZ = "America/Los_Angeles" // pre-populated cache

	ref := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := wc.SetCaptureTimes(context.Background(), ref, tzClient, solar); err != nil {
		t.Fatalf("SetCaptureTimes with cached timezone: %v", err)
	}
	wc.mu.RLock()
	got := wc.WebcamTZ
	wc.mu.RUnlock()
	if got != "America/Los_Angeles" {
		t.Errorf("WebcamTZ: want %q, got %q", "America/Los_Angeles", got)
	}
}

// TestSetCaptureTimes_refetchesInvalidCachedTimezone verifies that an
// unrecognizable stored timezone triggers a fresh API lookup.
func TestSetCaptureTimes_refetchesInvalidCachedTimezone(t *testing.T) {
	validTZ := "America/Los_Angeles"
	tzClient := &fixedTimezoneClient{tz: validTZ}
	solar := &fixedSolarClient{times: laFixedSolar()}

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 15)
	wc.WebcamTZ = "Not/A/ValidZone" // bad cached value

	ref := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := wc.SetCaptureTimes(context.Background(), ref, tzClient, solar); err != nil {
		t.Fatalf("SetCaptureTimes re-fetch: %v", err)
	}
	wc.mu.RLock()
	got := wc.WebcamTZ
	wc.mu.RUnlock()
	if got != validTZ {
		t.Errorf("WebcamTZ after re-fetch: want %q, got %q", validTZ, got)
	}
}

// TestSetCaptureTimes_loadLocationError confirms that an unrecognised timezone
// name returned by the API causes a LoadLocation error at schedule.go:66.
// This can only happen if the API returns a non-IANA-standard string; the
// embedded tzdata database means valid names always load successfully.
func TestSetCaptureTimes_loadLocationError(t *testing.T) {
	tzClient := &fixedTimezoneClient{tz: "Not/A/Real/Timezone"}
	solar := &fixedSolarClient{times: laFixedSolar()}
	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 15)
	// WebcamTZ is empty, so GetTimezone is called and returns the bogus name.

	err := wc.SetCaptureTimes(context.Background(), time.Now(), tzClient, solar)
	if err == nil {
		t.Error("expected error for unrecognised timezone name, got nil")
	}
	if !strings.Contains(err.Error(), "LoadLocation") {
		t.Errorf("error should mention LoadLocation: %v", err)
	}
}

func TestSetCaptureTimes_timezoneClientError(t *testing.T) {
	tzClient := &fixedTimezoneClient{err: fmt.Errorf("tz lookup failed")}
	solar := &fixedSolarClient{times: laFixedSolar()}
	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 15)

	err := wc.SetCaptureTimes(context.Background(), time.Now(), tzClient, solar)
	if err == nil {
		t.Error("expected error from tz client, got nil")
	}
}

// ---------------------------------------------------------------------------
// firstCaptureTime / lastCaptureTime — missing flag variants and error paths
// ---------------------------------------------------------------------------

func TestSetCaptureTimes_firstSunrise60(t *testing.T) {
	tzClient := &fixedTimezoneClient{tz: "America/Los_Angeles"}
	s := laFixedSolar()
	solar := &fixedSolarClient{times: s}
	wc := testWebcam(t, flagFirstSunrise60, flagLastSunset, 15)
	ref := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	if err := wc.SetCaptureTimes(context.Background(), ref, tzClient, solar); err != nil {
		t.Fatalf("SetCaptureTimes: %v", err)
	}
	want := s.Sunrise.Add(60 * time.Minute)
	wc.mu.RLock()
	got := wc.CaptureTimes[0]
	wc.mu.RUnlock()
	if !got.Equal(want) {
		t.Errorf("first capture: want %v, got %v", want, got)
	}
}

func TestSetCaptureTimes_lastSunset30(t *testing.T) {
	tzClient := &fixedTimezoneClient{tz: "America/Los_Angeles"}
	s := laFixedSolar()
	solar := &fixedSolarClient{times: s}
	wc := testWebcam(t, flagFirstSunrise, flagLastSunset30, 15)
	ref := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	if err := wc.SetCaptureTimes(context.Background(), ref, tzClient, solar); err != nil {
		t.Fatalf("SetCaptureTimes: %v", err)
	}
	want := s.Sunset.Add(-30 * time.Minute)
	wc.mu.RLock()
	last := wc.CaptureTimes[len(wc.CaptureTimes)-1]
	wc.mu.RUnlock()
	if !last.Equal(want) {
		t.Errorf("last capture: want %v, got %v", want, last)
	}
}

func TestSetCaptureTimes_lastTime(t *testing.T) {
	tzClient := &fixedTimezoneClient{tz: "America/Los_Angeles"}
	s := laFixedSolar()
	solar := &fixedSolarClient{times: s}

	wc := testWebcam(t, flagFirstSunrise, flagLastTime, 15)
	wc.LastTimeValue = "17:00"
	ref := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	if err := wc.SetCaptureTimes(context.Background(), ref, tzClient, solar); err != nil {
		t.Fatalf("SetCaptureTimes: %v", err)
	}
	// 17:00 PDT (UTC-7) = 00:00 UTC on 2026-06-02
	want := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	wc.mu.RLock()
	last := wc.CaptureTimes[len(wc.CaptureTimes)-1]
	wc.mu.RUnlock()
	if !last.Equal(want) {
		t.Errorf("last capture: want %v, got %v", want, last)
	}
}

// TestSetCaptureTimes_noFirstFlagError confirms SetCaptureTimes propagates the
// error from firstCaptureTime when FirstFlags is zero.
func TestSetCaptureTimes_noFirstFlagError(t *testing.T) {
	tzClient := &fixedTimezoneClient{tz: "America/Los_Angeles"}
	solar := &fixedSolarClient{times: laFixedSolar()}

	wc := newWebcam()
	wc.Name = "test"
	wc.Latitude = 34.0
	wc.Longitude = -118.0
	wc.WebcamTZ = "America/Los_Angeles"
	wc.FirstFlags = 0 // no first flag — firstCaptureTime will error
	wc.LastFlags = flagLastSunset
	ref := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	if err := wc.SetCaptureTimes(context.Background(), ref, tzClient, solar); err == nil {
		t.Error("expected error for no first flag, got nil")
	}
}

// TestSetCaptureTimes_noLastFlagError confirms SetCaptureTimes propagates the
// error from lastCaptureTime when LastFlags is zero.
func TestSetCaptureTimes_noLastFlagError(t *testing.T) {
	tzClient := &fixedTimezoneClient{tz: "America/Los_Angeles"}
	solar := &fixedSolarClient{times: laFixedSolar()}

	wc := newWebcam()
	wc.Name = "test"
	wc.Latitude = 34.0
	wc.Longitude = -118.0
	wc.WebcamTZ = "America/Los_Angeles"
	wc.FirstFlags = flagFirstSunrise
	wc.LastFlags = 0 // no last flag — lastCaptureTime will error
	ref := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	if err := wc.SetCaptureTimes(context.Background(), ref, tzClient, solar); err == nil {
		t.Error("expected error for no last flag, got nil")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func assertSorted(t *testing.T, times []time.Time) {
	t.Helper()
	for i := 1; i < len(times); i++ {
		if times[i].Before(times[i-1]) {
			t.Errorf("schedule not sorted: times[%d]=%v before times[%d]=%v",
				i, times[i], i-1, times[i-1])
		}
	}
}
