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
//	> 2 weeks     auto-suspend: goroutine exits with a   (tier 4)
//	              prominent log message; set
//	              "disabled": true in logscene.json to
//	              suppress on restart, or restart the
//	              server to try again.
//
// On success at any tier, all failure tracking resets immediately.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sort"
	"time"
)

const (
	backoffInitial = 1 * time.Second
	backoffMax     = 10 * time.Minute

	outageTier2After    = 24 * time.Hour       // switch from exponential to hourly
	outageTier3After    = 48 * time.Hour       // switch from hourly to daily
	outageAutoSuspendAfter = 14 * 24 * time.Hour // exit goroutine after 2 weeks
)

// capture is the long-running goroutine for one webcam.
func capture(ctx context.Context, wc *Webcam, pollInterval time.Duration, srv *server) {
	name := "capture." + wc.Name
	defer srv.webcamWg.Done()

	if wc.Disabled {
		log.Printf("%s: webcam is disabled — skipping", name)
		return
	}

	// Note whether timezone needs to be fetched so we can persist it afterward.
	wc.mu.RLock()
	tzWasEmpty := wc.WebcamTZ == ""
	wc.mu.RUnlock()

	if err := wc.SetCaptureTimes(ctx, time.Now(), srv.tz, srv.solar); err != nil {
		log.Printf("%s: SetCaptureTimes failed: %v — exiting.\n"+
			"  Restart the server to retry, or set \"disabled\": true in logscene.json to suppress.",
			name, err)
		return
	}

	if tzWasEmpty {
		if err := srv.webcams.Write(srv.config.Path, srv.validate); err != nil {
			log.Printf("%s: persist timezone to logscene.json: %v", name, err)
		} else {
			wc.mu.RLock()
			log.Printf("%s: cached timezone %q in logscene.json", name, wc.WebcamTZ)
			wc.mu.RUnlock()
		}
	}

	if err := wc.UpdateNextCapture(ctx, time.Now(), srv.tz, srv.solar); err != nil {
		log.Printf("%s: UpdateNextCapture failed: %v — exiting.\n"+
			"  Restart the server to retry, or set \"disabled\": true in logscene.json to suppress.",
			name, err)
		return
	}

	wc.mu.RLock()
	log.Printf("%s: tz=%s nextCapture=%s schedule(%d)=%v firstFlags=%b lastFlags=%b",
		name, wc.WebcamTZ, wc.CaptureTimes[wc.NextCapture],
		len(wc.CaptureTimes), wc.CaptureTimes, wc.FirstFlags, wc.LastFlags)
	wc.mu.RUnlock()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("%s: context cancelled — exiting", name)
			return
		case <-ticker.C:
			if wc.autoSuspendDue() {
				log.Printf("%s: auto-suspended after %v of consecutive failures — goroutine exiting.\n"+
					"  Set \"disabled\": true in logscene.json to suppress on restart,\n"+
					"  or restart the server to try again.",
					name, outageAutoSuspendAfter)
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
				wc.mu.RUnlock()
				effectiveNext := max(backoff, pollInterval)
				log.Printf("%s: CaptureImage failed (failing %v, next attempt in ~%v (backoff %v, poll interval %v)): %v",
					name, since, effectiveNext, backoff, pollInterval, err)
				continue
			}

			wc.recordSuccess()
			log.Printf("%s: captured %s (%d bytes)", name, key, size)

			if err := wc.UpdateNextCapture(ctx, time.Now(), srv.tz, srv.solar); err != nil {
				log.Printf("%s: UpdateNextCapture: %v — exiting.\n"+
					"  Restart the server to retry, or set \"disabled\": true in logscene.json to suppress.",
					name, err)
				return
			}
		}
	}
}

// shouldAttemptNow returns true if a capture is overdue AND enough time has
// elapsed since the last attempt per the current outage tier.
// Calling this without a failure streak is equivalent to IsTimeForCapture.
func (wc *Webcam) shouldAttemptNow() bool {
	wc.mu.RLock()
	defer wc.mu.RUnlock()

	if wc.NextCapture >= len(wc.CaptureTimes) {
		return false
	}
	if !time.Now().After(wc.CaptureTimes[wc.NextCapture]) {
		return false
	}
	if wc.FirstFailure.IsZero() {
		return true // no active failure streak — attempt immediately
	}
	interval := wc.currentRetryInterval()
	return time.Since(wc.LastAttempt) >= interval
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
}

// IsTimeForCapture returns true if the current wall-clock time is at or past
// the next scheduled capture time, ignoring outage backoff.
// Use shouldAttemptNow in the capture goroutine; this is for simple checks elsewhere.
func (wc *Webcam) IsTimeForCapture() bool {
	wc.mu.RLock()
	defer wc.mu.RUnlock()
	if wc.NextCapture >= len(wc.CaptureTimes) {
		return false
	}
	return time.Now().After(wc.CaptureTimes[wc.NextCapture])
}

// NextCaptureTime returns the scheduled time of the next capture.
func (wc *Webcam) NextCaptureTime() time.Time {
	wc.mu.RLock()
	defer wc.mu.RUnlock()
	return wc.CaptureTimes[wc.NextCapture]
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
	t := wc.CaptureTimes[wc.NextCapture]
	wc.mu.RUnlock()
	return baseDir + "/" + wc.Folder + "/" + wc.Name + " " + t.UTC().Format("20060102150405")
}

// UpdateNextCapture advances NextCapture to the first future time in CaptureTimes.
func (wc *Webcam) UpdateNextCapture(
	ctx context.Context,
	baseTime time.Time,
	tzClient TimezoneClient,
	solarClient SolarClient,
) error {
	wc.mu.Lock()
	if !sort.SliceIsSorted(wc.CaptureTimes, func(i, j int) bool {
		return wc.CaptureTimes[i].Before(wc.CaptureTimes[j])
	}) {
		sort.Slice(wc.CaptureTimes, func(i, j int) bool {
			return wc.CaptureTimes[i].Before(wc.CaptureTimes[j])
		})
	}

	wc.NextCapture = 0
	for _, t := range wc.CaptureTimes {
		if t.After(baseTime) {
			break
		}
		wc.NextCapture++
	}
	needsNewDay := wc.NextCapture >= len(wc.CaptureTimes)

	var tomorrowRef time.Time
	if needsNewDay && wc.WebcamLoc != nil {
		last := wc.CaptureTimes[len(wc.CaptureTimes)-1]
		wcTime := last.In(wc.WebcamLoc)
		y, m, d := wcTime.Date()
		tomorrowRef = time.Date(y, m, d+1, 12, 0, 0, 0, wc.WebcamLoc)
	}
	wc.mu.Unlock()

	if !needsNewDay {
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
		log.Printf("UpdateNextCapture: %q attempt %d/%d failed: %v, retrying in %v",
			wc.Name, i+1, len(retries)+1, err, retries[i])
		select {
		case <-time.After(retries[i]):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	wc.mu.Lock()
	wc.NextCapture = 0
	log.Printf("UpdateNextCapture: %q schedule for tomorrow set; captures(%d)=%v",
		wc.Name, len(wc.CaptureTimes), wc.CaptureTimes)
	wc.mu.Unlock()
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
