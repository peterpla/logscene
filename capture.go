// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// capture.go contains the per-webcam goroutine, image retrieval, and the
// graduated outage-backoff policy.
//
// Outage tiers (time since first consecutive failure):
//
//	0 – 24 h      exponential backoff, capped at 10 min  (tier 1)
//	24 h – 2 d    retry once per hour                    (tier 2)
//	2 d – 2 weeks retry once per day                     (tier 3)
//	> 2 weeks     auto-suspend: goroutine exits           (tier 4)
//
// On success at any tier, all failure tracking resets immediately.
//
// Auto-suspend (tier 4) triggers an in-app modal offering two choices:
//   - Disable: sets Disabled=true in logscene.json; status indicator goes red.
//   - Keep Trying: schedules a fresh goroutine start at the next idle window
//     with all backoff state cleared. If failures continue for another 2 weeks,
//     the modal appears again.
//
// Idle window: the period after DayLast and before DayFirst the following day,
// when no captures are scheduled. Goroutine restarts are initiated during this
// window to avoid interrupting active capture schedules.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	backoffInitial = 1 * time.Second
	backoffMax     = 10 * time.Minute

	outageTier2After       = 24 * time.Hour       // switch from exponential to hourly
	outageTier3After       = 48 * time.Hour        // switch from hourly to daily
	outageAutoSuspendAfter = 14 * 24 * time.Hour   // exit goroutine after 2 weeks
)

// capture is the long-running goroutine for one webcam.
func capture(ctx context.Context, wc *Webcam, pollInterval time.Duration, srv *server) {
	defer srv.webcamWg.Done()

	if wc.Disabled {
		slog.Info("webcam disabled — no captures", "webcam", wc.Name)
		slog.Debug("webcam disabled at startup, goroutine exiting", "webcam", wc.Name)
		srv.status.Set(wc.Name, StatusDisabled, "", "")
		return
	}

	// Note whether timezone needs to be fetched so we can persist it afterward.
	wc.mu.RLock()
	tzWasEmpty := wc.WebcamTZ == ""
	wc.mu.RUnlock()

	if err := wc.SetCaptureTimes(ctx, time.Now(), srv.tz, srv.solar); err != nil {
		slog.Debug("SetCaptureTimes failed at startup, goroutine exiting",
			"webcam", wc.Name,
			"error", err)
		return
	}

	if tzWasEmpty {
		if err := srv.webcams.Write(srv.config.Path, srv.validate); err != nil {
			slog.Debug("failed to persist timezone to logscene.json — will re-fetch on next startup",
				"webcam", wc.Name,
				"error", err)
		}
	}

	// If we started up after today's last capture, roll over to tomorrow immediately.
	wc.mu.Lock()
	rollover := wc.NextCaptureAt.IsZero() && !wc.DayLast.IsZero() && wc.WebcamLoc != nil
	var tomorrowRef time.Time
	if rollover {
		wcTime := wc.DayLast.In(wc.WebcamLoc)
		y, m, d := wcTime.Date()
		tomorrowRef = time.Date(y, m, d+1, 0, 0, 1, 0, wc.WebcamLoc)
	}
	wc.mu.Unlock()
	if rollover {
		if err := wc.SetCaptureTimes(ctx, tomorrowRef, srv.tz, srv.solar); err != nil {
			slog.Debug("SetCaptureTimes (tomorrow) failed at startup, goroutine exiting",
				"webcam", wc.Name,
				"error", err)
			return
		}
	}

	wc.mu.RLock()
	snapTZ := wc.WebcamTZ
	snapLoc := wc.WebcamLoc
	snapSunrise := wc.SunriseUTC
	snapFirst := wc.DayFirst
	snapLast := wc.DayLast
	snapNext := wc.NextCaptureAt
	snapInterval := wc.IntervalMinutes
	snapFirstFlags := wc.FirstFlags
	snapLastFlags := wc.LastFlags
	wc.mu.RUnlock()

	// Seed CaptureCountToday by counting existing capture files for today's
	// schedule date (handles mid-day restarts). Compute ScheduledCountToday
	// from the day's first/last capture times and interval.
	if !snapFirst.IsZero() {
		dateStr := snapFirst.UTC().Format("20060102")
		matches, _ := filepath.Glob(filepath.Join(srv.config.BaseDir, wc.Folder, "*"+dateStr+"*"))
		interval := time.Duration(snapInterval) * time.Minute
		wc.mu.Lock()
		wc.CaptureCountToday = len(matches)
		if interval > 0 {
			wc.ScheduledCountToday = int(snapLast.Sub(snapFirst)/interval) + 1
		}
		wc.mu.Unlock()
	}

	var solarDate string
	if snapLoc != nil {
		solarDate = snapSunrise.In(snapLoc).Format("2006-01-02")
	}
	slog.Debug("webcam ready — entering capture loop",
		"webcam", wc.Name,
		"tz", snapTZ,
		"solar_date", solarDate,
		"day_first", snapFirst.UTC().Format("15:04:05"),
		"day_last", snapLast.UTC().Format("15:04:05"),
		"next_capture_at", snapNext.UTC().Format("15:04:05"),
		"interval_minutes", snapInterval,
		"first_flags", fmt.Sprintf("%b", snapFirstFlags),
		"last_flags", fmt.Sprintf("%b", snapLastFlags))

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// lastVotedTier tracks which tier we last voted StatusIssues for, so we
	// only vote once per tier transition (1→2 at 24 h, 2→3 at 48 h).
	var lastVotedTier int

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if wc.autoSuspendDue() {
				slog.Info("webcam could not be reached for 2 consecutive weeks", "webcam", wc.Name)
				wc.mu.RLock()
				firstFailure := wc.FirstFailure
				wc.mu.RUnlock()
				slog.Debug("auto-suspend threshold reached, goroutine exiting — awaiting user decision via modal",
					"webcam", wc.Name,
					"first_failure", firstFailure.UTC().Format("2006-01-02"),
					"outage_duration", time.Since(firstFailure).Truncate(time.Minute))
				return
			}

			if !wc.shouldAttemptNow() {
				continue
			}

			key, size, err := wc.CaptureImage(ctx, srv.fetcher, srv.storage, srv.config.BaseDir)
			if err != nil {
				wc.recordFailure()
				wc.mu.RLock()
				since := time.Since(wc.FirstFailure).Truncate(time.Minute)
				backoff := wc.nextRetryInterval()
				tier := captureTierNum(wc.FirstFailure)
				wc.mu.RUnlock()
				effectiveNext := max(backoff, pollInterval)
				slog.Debug("CaptureImage failed",
					"webcam", wc.Name,
					"failure_class", fcUnreachable,
					"error", err,
					"outage_duration", since,
					"next_retry_in", effectiveNext)
				// Vote StatusIssues once per tier transition (tier 1→2 at 24 h, 2→3 at 48 h).
				if tier > lastVotedTier {
					lastVotedTier = tier
					srv.status.Set(wc.Name, StatusIssues, "", "")
				}
				continue
			}

			wc.recordSuccess()
			lastVotedTier = 0
			srv.status.Set(wc.Name, StatusActive, "", "")
			slog.Debug("captured", "webcam", wc.Name, "filePath", key, "bytes", size)

			if err := wc.UpdateNextCapture(ctx, srv.tz, srv.solar); err != nil {
				slog.Debug("UpdateNextCapture failed, goroutine exiting",
					"webcam", wc.Name,
					"failure_class", fcNetworkAPI,
					"error", err)
				return
			}
		}
	}
}

// captureTierNum returns the current outage tier (1/2/3) based on how long
// the failure streak has lasted, or 0 if there is no active streak.
func captureTierNum(firstFailure time.Time) int {
	if firstFailure.IsZero() {
		return 0
	}
	switch elapsed := time.Since(firstFailure); {
	case elapsed < outageTier2After:
		return 1
	case elapsed < outageTier3After:
		return 2
	default:
		return 3
	}
}

// shouldAttemptNow returns true if a capture is overdue AND enough time has
// elapsed since the last attempt per the current outage tier.
// Calling this without a failure streak is equivalent to IsTimeForCapture.
func (wc *Webcam) shouldAttemptNow() bool {
	wc.mu.RLock()
	defer wc.mu.RUnlock()

	if wc.NextCaptureAt.IsZero() || !time.Now().After(wc.NextCaptureAt) {
		return false
	}
	if wc.FirstFailure.IsZero() {
		return true // no active failure streak — attempt immediately
	}
	return time.Since(wc.LastAttempt) >= wc.currentRetryInterval()
}

// autoSuspendDue returns true once the failure streak has lasted 2 weeks.
func (wc *Webcam) autoSuspendDue() bool {
	wc.mu.RLock()
	defer wc.mu.RUnlock()
	return !wc.FirstFailure.IsZero() && time.Since(wc.FirstFailure) >= outageAutoSuspendAfter
}

// currentRetryInterval returns the retry interval for the current outage tier.
// Must be called with wc.mu held (read or write).
func (wc *Webcam) currentRetryInterval() time.Duration {
	if wc.FirstFailure.IsZero() {
		return 0
	}
	switch elapsed := time.Since(wc.FirstFailure); {
	case elapsed < outageTier2After:
		return wc.Backoff // tier 1: exponential, up to 10 min
	case elapsed < outageTier3After:
		return time.Hour // tier 2: once per hour
	default:
		return 24 * time.Hour // tier 3: once per day
	}
}

// nextRetryInterval is the public wrapper used for logging (acquires the lock).
func (wc *Webcam) nextRetryInterval() time.Duration {
	return wc.currentRetryInterval()
}

// recordFailure updates failure tracking after a failed capture attempt.
// It sets FirstFailure on the first call and updates exponential backoff
// while in tier 1; tiers 2 and 3 use fixed intervals so Backoff is not changed.
func (wc *Webcam) recordFailure() {
	wc.mu.Lock()
	defer wc.mu.Unlock()

	if wc.FirstFailure.IsZero() {
		wc.FirstFailure = time.Now()
	}
	wc.LastAttempt = time.Now()

	// Only advance exponential backoff in tier 1.
	if time.Since(wc.FirstFailure) < outageTier2After {
		if wc.Backoff == 0 {
			wc.Backoff = backoffInitial
		} else {
			wc.Backoff *= 2
		}
		if wc.Backoff > backoffMax {
			wc.Backoff = backoffMax
		}
	}
}

// recordSuccess resets all failure tracking after a successful capture.
func (wc *Webcam) recordSuccess() {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	wc.FirstFailure = time.Time{}
	wc.LastAttempt = time.Time{}
	wc.Backoff = 0
	wc.CaptureCountToday++
}

// IsTimeForCapture returns true if the current wall-clock time is at or past
// the next scheduled capture time, ignoring outage backoff.
// Use shouldAttemptNow in the capture goroutine; this is for simple checks elsewhere.
func (wc *Webcam) IsTimeForCapture() bool {
	wc.mu.RLock()
	defer wc.mu.RUnlock()
	return !wc.NextCaptureAt.IsZero() && time.Now().After(wc.NextCaptureAt)
}

// NextCaptureTime returns the scheduled time of the next capture.
func (wc *Webcam) NextCaptureTime() time.Time {
	wc.mu.RLock()
	defer wc.mu.RUnlock()
	return wc.NextCaptureAt
}

// CaptureImage fetches one image from the webcam and writes it to storage.
// Dispatch is based on SourceType; an empty SourceType is treated as "url"
// for backward compatibility with existing logscene.json entries.
func (wc *Webcam) CaptureImage(ctx context.Context, fetcher ImageFetcher, store Storage, baseDir string) (string, int64, error) {
	key := wc.targetKey(baseDir)
	st := wc.SourceType
	if st == "" {
		st = "url"
	}

	switch st {
	case "usb":
		key += ".jpg"
		size, err := captureViaFfmpeg(ctx,
			[]string{"-f", "dshow", "-i", "video=" + wc.DeviceName, "-frames:v", "1"},
			store, key)
		return key, size, err

	case "stream":
		key += ".jpg"
		size, err := captureViaFfmpeg(ctx,
			[]string{"-i", wc.URL, "-frames:v", "1"},
			store, key)
		return key, size, err

	default: // "url"
		body, contentType, err := fetcher.Fetch(ctx, wc.URL)
		if err != nil {
			return "", 0, fmt.Errorf("CaptureImage: fetch: %w", err)
		}
		defer body.Close()
		key += extensionForContentType(contentType)
		counter := &countingReader{r: body}
		if err := store.Write(ctx, key, counter); err != nil {
			return "", 0, fmt.Errorf("CaptureImage: store: %w", err)
		}
		return key, counter.n, nil
	}
}

// captureViaFfmpeg runs ffmpeg with the given args, captures a single frame
// to a temp file, then streams that file into storage.
func captureViaFfmpeg(ctx context.Context, args []string, store Storage, key string) (int64, error) {
	tmp, err := os.CreateTemp("", "logscene-capture-*.jpg")
	if err != nil {
		return 0, fmt.Errorf("captureViaFfmpeg: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	cmdArgs := append([]string{"-y"}, args...)
	cmdArgs = append(cmdArgs, "-update", "1", tmpPath)
	cmd := exec.CommandContext(ctx, "ffmpeg", cmdArgs...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("captureViaFfmpeg: ffmpeg: %w: %s", err, bytes.TrimSpace(out))
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return 0, fmt.Errorf("captureViaFfmpeg: open output: %w", err)
	}
	defer f.Close()

	counter := &countingReader{r: f}
	if err := store.Write(ctx, key, counter); err != nil {
		return 0, fmt.Errorf("captureViaFfmpeg: store: %w", err)
	}
	return counter.n, nil
}

// targetKey builds the storage key: baseDir/folder/Name YYYYMMDDhhmmss
func (wc *Webcam) targetKey(baseDir string) string {
	wc.mu.RLock()
	t := wc.NextCaptureAt
	wc.mu.RUnlock()
	return baseDir + "/" + wc.Folder + "/" + wc.Name + " " + t.UTC().Format("20060102150405")
}

// UpdateNextCapture advances NextCaptureAt by one interval after a successful capture.
// If today's schedule is exhausted (just captured DayLast), fetches tomorrow's schedule.
func (wc *Webcam) UpdateNextCapture(ctx context.Context, tzClient TimezoneClient, solarClient SolarClient) error {
	wc.mu.Lock()
	var tomorrowRef time.Time
	if wc.NextCaptureAt.Equal(wc.DayLast) {
		// Just captured the last slot — done for today; need tomorrow's schedule.
		if wc.WebcamLoc != nil {
			wcTime := wc.DayLast.In(wc.WebcamLoc)
			y, m, d := wcTime.Date()
			tomorrowRef = time.Date(y, m, d+1, 0, 0, 1, 0, wc.WebcamLoc)
		}
		wc.NextCaptureAt = time.Time{}
	} else {
		interval := time.Duration(wc.IntervalMinutes) * time.Minute
		next := wc.NextCaptureAt.Add(interval)
		if next.After(wc.DayLast) {
			wc.NextCaptureAt = wc.DayLast
		} else {
			wc.NextCaptureAt = next
		}
	}
	wc.mu.Unlock()

	if tomorrowRef.IsZero() {
		return nil
	}

	retries := []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second}
	var err error
	for i := 0; ; i++ {
		err = wc.SetCaptureTimes(ctx, tomorrowRef, tzClient, solarClient)
		if err == nil {
			break
		}
		if i >= len(retries) {
			return fmt.Errorf("UpdateNextCapture: %q SetCaptureTimes failed after %d attempts: %w",
				wc.Name, len(retries)+1, err)
		}
		slog.Debug("UpdateNextCapture: SetCaptureTimes attempt failed, retrying",
			"webcam", wc.Name,
			"failure_class", fcNetworkAPI,
			"api", "sunrise-sunset",
			"attempt", i+1,
			"maxAttempts", len(retries)+1,
			"error", err,
			"retryIn", retries[i])
		select {
		case <-time.After(retries[i]):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Reset daily counters for the new day now that SetCaptureTimes succeeded.
	wc.mu.Lock()
	wc.CaptureCountToday = 0
	if interval := time.Duration(wc.IntervalMinutes) * time.Minute; interval > 0 {
		wc.ScheduledCountToday = int(wc.DayLast.Sub(wc.DayFirst)/interval) + 1
	}
	wc.mu.Unlock()

	wc.mu.RLock()
	snapFirst := wc.DayFirst
	snapLast := wc.DayLast
	snapNext := wc.NextCaptureAt
	snapTZ := wc.WebcamTZ
	wc.mu.RUnlock()
	slog.Debug("UpdateNextCapture: tomorrow set",
		"webcam", wc.Name,
		"dayFirst", snapFirst.UTC().Format("15:04:05"),
		"dayLast", snapLast.UTC().Format("15:04:05"),
		"nextCaptureAt", snapNext.UTC().Format("15:04:05"),
		"tz", snapTZ)
	return nil
}

// ---------------------------------------------------------------------------
// countingReader wraps an io.Reader and records how many bytes were read.
// ---------------------------------------------------------------------------

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
