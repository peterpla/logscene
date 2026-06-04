// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// schedule.go computes the daily ordered list of capture times for a Webcam.
//
// Key correctness invariant: all time.Time values in CaptureTimes are stored
// in UTC. The webcam's timezone is used only to (a) determine the correct
// calendar date for the solar-times API query and (b) interpret FirstTimeValue/
// LastTimeValue clock strings. Comparisons with time.Now() are always correct
// regardless of the timezone of the machine running this server.

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SetCaptureTimes computes and stores the full ordered capture schedule for the
// calendar day that referenceTime falls on in the webcam's timezone.
//
// It must be called when CaptureTimes is empty or all times have passed.
// It fetches the webcam timezone via tzClient, then solar times via solarClient,
// then builds the schedule according to the First*/Last*/Additional settings.
func (wc *Webcam) SetCaptureTimes(
	ctx context.Context,
	referenceTime time.Time,
	tzClient TimezoneClient,
	solarClient SolarClient,
) error {
	// Guard: refuse to overwrite a schedule that still has future times.
	// Also read the cached timezone under the same lock to avoid a separate RLock.
	wc.mu.RLock()
	n := len(wc.CaptureTimes)
	if n > 0 && time.Now().Before(wc.CaptureTimes[n-1]) {
		wc.mu.RUnlock()
		return fmt.Errorf("SetCaptureTimes: %q still has future capture times", wc.Name)
	}
	cachedTZ := wc.WebcamTZ
	wc.mu.RUnlock()

	// 1. Resolve webcam timezone.
	//    Use the cached IANA name when present — the timezone name never changes
	//    for a fixed location, and DST is handled automatically by time.LoadLocation.
	//    Only call the API on a cache miss or if the stored name is unrecognizable.
	var tz string
	var loc *time.Location
	if cachedTZ != "" {
		if l, err := time.LoadLocation(cachedTZ); err == nil {
			tz, loc = cachedTZ, l
		} else {
			log.Printf("SetCaptureTimes: %q cached timezone %q unrecognized (%v), re-fetching",
				wc.Name, cachedTZ, err)
		}
	}
	if loc == nil {
		var err error
		tz, err = tzClient.GetTimezone(ctx, wc.Latitude, wc.Longitude)
		if err != nil {
			return fmt.Errorf("SetCaptureTimes: GetTimezone: %w", err)
		}
		loc, err = time.LoadLocation(tz)
		if err != nil {
			return fmt.Errorf("SetCaptureTimes: LoadLocation(%q): %w", tz, err)
		}
	}

	// 2. Determine today's date *in the webcam's timezone* — this is the fix for
	//    the cloud-server timezone bug: a server at UTC+12 must not use its own
	//    calendar date when querying solar times for a webcam in UTC-8.
	webcamNow := referenceTime.In(loc)

	// 3. Fetch solar times for that local date.
	solar, err := solarClient.GetSolarTimes(ctx, wc.Latitude, wc.Longitude, webcamNow)
	if err != nil {
		return fmt.Errorf("SetCaptureTimes: GetSolarTimes: %w", err)
	}

	// 4. Build the schedule under the write lock.
	wc.mu.Lock()
	defer wc.mu.Unlock()

	wc.WebcamTZ = tz
	wc.WebcamLoc = loc
	wc.SunriseUTC = solar.Sunrise
	wc.SolarNoonUTC = solar.SolarNoon
	wc.SunsetUTC = solar.Sunset
	wc.CaptureTimes = wc.CaptureTimes[:0] // clear, reuse backing array

	first, err := wc.firstCaptureTime(webcamNow)
	if err != nil {
		return err
	}
	last, err := wc.lastCaptureTime(webcamNow)
	if err != nil {
		return err
	}

	wc.CaptureTimes = buildSchedule(first, last, solar.SolarNoon, wc.Additional)

	log.Printf("SetCaptureTimes: %q tz=%s captures(%d)=%v",
		wc.Name, tz, len(wc.CaptureTimes), wc.CaptureTimes)
	return nil
}

// firstCaptureTime returns the UTC time of the first capture for webcamNow's date.
// webcamNow must already be in the webcam's timezone.
func (wc *Webcam) firstCaptureTime(webcamNow time.Time) (time.Time, error) {
	switch {
	case wc.FirstFlags&flagFirstSunrise != 0:
		return wc.SunriseUTC, nil
	case wc.FirstFlags&flagFirstSunrise30 != 0:
		return wc.SunriseUTC.Add(30 * time.Minute), nil
	case wc.FirstFlags&flagFirstSunrise60 != 0:
		return wc.SunriseUTC.Add(60 * time.Minute), nil
	case wc.FirstFlags&flagFirstTime != 0:
		return parseTimeOfDay(wc.FirstTimeValue, webcamNow, wc.WebcamLoc)
	}
	return time.Time{}, fmt.Errorf("firstCaptureTime: no first-capture flag set")
}

// lastCaptureTime returns the UTC time of the last capture for webcamNow's date.
func (wc *Webcam) lastCaptureTime(webcamNow time.Time) (time.Time, error) {
	switch {
	case wc.LastFlags&flagLastSunset != 0:
		return wc.SunsetUTC, nil
	case wc.LastFlags&flagLastSunset30 != 0:
		return wc.SunsetUTC.Add(-30 * time.Minute), nil
	case wc.LastFlags&flagLastSunset60 != 0:
		return wc.SunsetUTC.Add(-60 * time.Minute), nil
	case wc.LastFlags&flagLastTime != 0:
		return parseTimeOfDay(wc.LastTimeValue, webcamNow, wc.WebcamLoc)
	}
	return time.Time{}, fmt.Errorf("lastCaptureTime: no last-capture flag set")
}

// parseTimeOfDay parses an "HH:MM" string as a time on the same calendar date
// as webcamNow (which must be in loc), returning the result in UTC.
func parseTimeOfDay(hhMM string, webcamNow time.Time, loc *time.Location) (time.Time, error) {
	parts := strings.SplitN(hhMM, ":", 2)
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("parseTimeOfDay: %q is not HH:MM", hhMM)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return time.Time{}, fmt.Errorf("parseTimeOfDay: invalid hour in %q", hhMM)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return time.Time{}, fmt.Errorf("parseTimeOfDay: invalid minute in %q", hhMM)
	}
	y, mo, d := webcamNow.Date()
	t := time.Date(y, mo, d, h, m, 0, 0, loc)
	return t.UTC(), nil
}

// buildSchedule returns the full ordered capture schedule given the first capture,
// last capture, solar noon, and number of additional (intermediate) captures.
//
// Distribution rules:
//
//	additional == 0 → [first, last]
//	additional == 1 → [first, solarNoon, last]
//	additional even → first + additional evenly-spaced times + last (no forced noon)
//	additional odd  → first + (n-1)/2 times + solarNoon + (n-1)/2 times + last
func buildSchedule(first, last, solarNoon time.Time, additional int) []time.Time {
	times := []time.Time{first}

	switch {
	case additional == 0:
		// nothing between first and last

	case additional == 1:
		times = append(times, solarNoon.Truncate(time.Second))

	case additional%2 == 0:
		times = append(times, splitTimes(first, last, additional)...)

	default: // odd, >= 3
		half := (additional - 1) / 2
		times = append(times, splitTimes(first, solarNoon, half)...)
		times = append(times, solarNoon.Truncate(time.Second))
		times = append(times, splitTimes(solarNoon, last, half)...)
	}

	times = append(times, last)

	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
	return times
}

// splitTimes returns n evenly-spaced times strictly between first and last.
// Times are truncated to whole seconds.
func splitTimes(first, last time.Time, n int) []time.Time {
	if n <= 0 {
		return nil
	}
	totalSecs := last.Unix() - first.Unix()
	interval := totalSecs / int64(n+1)

	result := make([]time.Time, n)
	base := first
	for i := 0; i < n; i++ {
		next := base.Add(time.Duration(interval) * time.Second).Truncate(time.Second)
		result[i] = next
		base = next
	}
	return result
}
