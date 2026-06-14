// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// schedule.go computes the daily capture schedule bounds for a Webcam.
//
// Key correctness invariant: all time.Time values are stored in UTC. The
// webcam's timezone is used only to (a) determine the correct calendar date
// for the solar-times API query and (b) interpret FirstTimeValue/LastTimeValue
// clock strings. Comparisons with time.Now() are always correct regardless of
// the timezone of the machine running this server.

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// ErrMalformedTimezone is returned by SetCaptureTimes when the timezone API
// returns a string that time.LoadLocation cannot parse.
type ErrMalformedTimezone struct {
	TZ  string
	Err error
}

func (e *ErrMalformedTimezone) Error() string {
	return fmt.Sprintf("SetCaptureTimes: LoadLocation(%q): %v", e.TZ, e.Err)
}

func (e *ErrMalformedTimezone) Unwrap() error { return e.Err }

// SetCaptureTimes computes and stores DayFirst, DayLast, and NextCaptureAt for
// the calendar day that referenceTime falls on in the webcam's timezone.
//
// NextCaptureAt is fast-forwarded to the first scheduled time at or after
// referenceTime — so starting up mid-day lands on the correct next slot rather
// than replaying every missed interval from sunrise.
//
// It must be called when NextCaptureAt is zero or past DayLast.
// It fetches the webcam timezone via tzClient, then solar times via solarClient.
func (wc *Webcam) SetCaptureTimes(
	ctx context.Context,
	referenceTime time.Time,
	tzClient TimezoneClient,
	solarClient SolarClient,
) error {
	// Guard: refuse to overwrite a schedule that still has future times.
	// Also read the cached timezone under the same lock to avoid a separate RLock.
	wc.mu.RLock()
	if !wc.NextCaptureAt.IsZero() && wc.NextCaptureAt.After(time.Now()) {
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
			slog.Debug("cached timezone unrecognized, re-fetching",
				"webcam", wc.Name,
				"cachedTZ", cachedTZ,
				"error", err)
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
			return &ErrMalformedTimezone{TZ: tz, Err: err}
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

	// 4. Store schedule bounds under the write lock, then log outside the lock
	//    to avoid holding it across a file write.
	wc.mu.Lock()

	wc.WebcamTZ = tz
	wc.WebcamLoc = loc
	wc.SunriseUTC = solar.Sunrise
	wc.SolarNoonUTC = solar.SolarNoon
	wc.SunsetUTC = solar.Sunset

	first, err := wc.firstCaptureTime(webcamNow)
	if err != nil {
		wc.mu.Unlock()
		return err
	}
	last, err := wc.lastCaptureTime(webcamNow)
	if err != nil {
		wc.mu.Unlock()
		return err
	}

	wc.DayFirst = first
	wc.DayLast = last
	wc.NextCaptureAt = firstFutureCapture(first, last, wc.IntervalMinutes, referenceTime)

	snapFirst, snapLast, snapNext := wc.DayFirst, wc.DayLast, wc.NextCaptureAt
	snapInterval, snapFirstFlags, snapLastFlags := wc.IntervalMinutes, wc.FirstFlags, wc.LastFlags
	wc.mu.Unlock()

	slog.Debug("schedule computed",
		"webcam", wc.Name,
		"scheduleDate", webcamNow.Format("2006-01-02"),
		"tz", tz,
		"intervalMin", snapInterval,
		"firstFlags", snapFirstFlags,
		"lastFlags", snapLastFlags,
		"sunrise", solar.Sunrise.UTC().Format("15:04:05"),
		"solarNoon", solar.SolarNoon.UTC().Format("15:04:05"),
		"sunset", solar.Sunset.UTC().Format("15:04:05"),
		"dayFirst", snapFirst.UTC().Format("15:04:05"),
		"dayLast", snapLast.UTC().Format("15:04:05"),
		"nextCapture", snapNext.UTC().Format("15:04:05"))
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

// firstFutureCapture returns the first scheduled capture at or after now.
// Returns zero time if now is past last (done for today).
func firstFutureCapture(first, last time.Time, intervalMinutes int, now time.Time) time.Time {
	if !now.After(first) {
		return first
	}
	if !now.Before(last) {
		return time.Time{} // past last capture — done for today
	}
	interval := time.Duration(intervalMinutes) * time.Minute
	n := int64(now.Sub(first))/int64(interval) + 1
	next := first.Add(time.Duration(n) * interval)
	if next.After(last) {
		return last
	}
	return next
}
