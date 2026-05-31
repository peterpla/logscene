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
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// splitTimes
// ---------------------------------------------------------------------------

func TestSplitTimes_zero(t *testing.T) {
	result := splitTimes(time.Unix(0, 0), time.Unix(3600, 0), 0)
	if len(result) != 0 {
		t.Errorf("want 0 results, got %d", len(result))
	}
}

func TestSplitTimes_one(t *testing.T) {
	first := time.Unix(0, 0)
	last := time.Unix(3600, 0)
	result := splitTimes(first, last, 1)
	if len(result) != 1 {
		t.Fatalf("want 1, got %d", len(result))
	}
	mid := time.Unix(1800, 0)
	if !result[0].Equal(mid) {
		t.Errorf("want %v, got %v", mid, result[0])
	}
}

func TestSplitTimes_three(t *testing.T) {
	first := time.Unix(0, 0)
	last := time.Unix(4000, 0)
	result := splitTimes(first, last, 3)
	if len(result) != 3 {
		t.Fatalf("want 3, got %d", len(result))
	}
	for i := 1; i < len(result); i++ {
		if !result[i].After(result[i-1]) {
			t.Errorf("result[%d]=%v not after result[%d]=%v", i, result[i], i-1, result[i-1])
		}
	}
	for i, r := range result {
		if !r.After(first) || !r.Before(last) {
			t.Errorf("result[%d]=%v not strictly between %v and %v", i, r, first, last)
		}
	}
}

// ---------------------------------------------------------------------------
// buildSchedule
// ---------------------------------------------------------------------------

func TestBuildSchedule_additional0(t *testing.T) {
	solar := laFixedSolar()
	schedule := buildSchedule(solar.Sunrise, solar.Sunset, solar.SolarNoon, 0)
	if len(schedule) != 2 {
		t.Fatalf("want 2 captures, got %d", len(schedule))
	}
	if !schedule[0].Equal(solar.Sunrise) {
		t.Errorf("first: want %v, got %v", solar.Sunrise, schedule[0])
	}
	if !schedule[1].Equal(solar.Sunset) {
		t.Errorf("last: want %v, got %v", solar.Sunset, schedule[1])
	}
}

func TestBuildSchedule_additional1(t *testing.T) {
	solar := laFixedSolar()
	schedule := buildSchedule(solar.Sunrise, solar.Sunset, solar.SolarNoon, 1)
	if len(schedule) != 3 {
		t.Fatalf("want 3 captures, got %d: %v", len(schedule), schedule)
	}
	wantNoon := solar.SolarNoon.Truncate(time.Second)
	if !schedule[1].Equal(wantNoon) {
		t.Errorf("middle: want noon %v, got %v", wantNoon, schedule[1])
	}
}

func TestBuildSchedule_additionalEven(t *testing.T) {
	solar := laFixedSolar()
	for _, n := range []int{2, 4, 6} {
		schedule := buildSchedule(solar.Sunrise, solar.Sunset, solar.SolarNoon, n)
		want := n + 2
		if len(schedule) != want {
			t.Errorf("additional=%d: want %d captures, got %d", n, want, len(schedule))
		}
		assertSorted(t, schedule)
	}
}

func TestBuildSchedule_additionalOdd(t *testing.T) {
	solar := laFixedSolar()
	for _, n := range []int{3, 5, 7} {
		schedule := buildSchedule(solar.Sunrise, solar.Sunset, solar.SolarNoon, n)
		want := n + 2
		if len(schedule) != want {
			t.Errorf("additional=%d: want %d captures, got %d", n, want, len(schedule))
		}
		assertSorted(t, schedule)
		wantNoon := solar.SolarNoon.Truncate(time.Second)
		count := 0
		for _, r := range schedule {
			if r.Equal(wantNoon) {
				count++
			}
		}
		if count != 1 {
			t.Errorf("additional=%d: solar noon %v appears %d times in %v", n, wantNoon, count, schedule)
		}
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

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 0)

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
	wc := testWebcam(t, flagFirstSunrise30, flagLastSunset, 0)
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
	wc := testWebcam(t, flagFirstSunrise, flagLastSunset60, 0)
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

	wc := testWebcam(t, flagFirstTime, flagLastSunset, 0)
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
	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 0)

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

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 0)
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

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 0)
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

func TestSetCaptureTimes_timezoneClientError(t *testing.T) {
	tzClient := &fixedTimezoneClient{err: fmt.Errorf("tz lookup failed")}
	solar := &fixedSolarClient{times: laFixedSolar()}
	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 0)

	err := wc.SetCaptureTimes(context.Background(), time.Now(), tzClient, solar)
	if err == nil {
		t.Error("expected error from tz client, got nil")
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
