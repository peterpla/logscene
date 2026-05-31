package main

// capture.go contains the per-webcam goroutine, image retrieval, backoff policy,
// and the logic that advances to the next capture time (rolling over to tomorrow
// when today's schedule is exhausted).

import (
	"context"
	"fmt"
	"io"
	"log"
	"sort"
	"time"
)

const (
	backoffInitial = 1 * time.Second
	backoffMax     = 10 * time.Minute
)

// capture is the long-running goroutine for one webcam. It polls on the
// configured interval and captures an image whenever the scheduled time arrives.
// It calls srv.wg.Done() before returning so the WaitGroup is always balanced.
func capture(ctx context.Context, wc *Webcam, pollInterval time.Duration, srv *server) {
	name := "capture." + wc.Name
	defer srv.wg.Done()

	if err := wc.SetCaptureTimes(ctx, time.Now(), srv.tz, srv.solar); err != nil {
		log.Printf("%s: SetCaptureTimes failed: %v — exiting", name, err)
		return
	}
	if err := wc.UpdateNextCapture(ctx, time.Now(), srv.tz, srv.solar); err != nil {
		log.Printf("%s: UpdateNextCapture failed: %v — exiting", name, err)
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
			if !wc.IsTimeForCapture() {
				continue
			}

			// Apply backoff before attempting the capture.
			wc.mu.RLock()
			backoff := wc.Backoff
			wc.mu.RUnlock()
			if backoff > 0 {
				log.Printf("%s: backing off %v", name, backoff)
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					log.Printf("%s: context cancelled during backoff — exiting", name)
					return
				}
			}

			key, size, err := wc.CaptureImage(ctx, srv.fetcher, srv.storage)
			if err != nil {
				log.Printf("%s: CaptureImage: %v", name, err)
				wc.AdjustBackoff()
				continue
			}
			wc.mu.Lock()
			wc.Backoff = 0
			wc.mu.Unlock()
			log.Printf("%s: captured %s (%d bytes)", name, key, size)

			if err := wc.UpdateNextCapture(ctx, time.Now(), srv.tz, srv.solar); err != nil {
				log.Printf("%s: UpdateNextCapture: %v — exiting", name, err)
				return
			}
		}
	}
}

// CaptureImage fetches one image from the webcam and writes it to storage.
// The storage key is based on the *scheduled* capture time, not wall clock,
// so the filename always matches the intended time even if the capture is late.
// Returns the storage key and number of bytes written.
func (wc *Webcam) CaptureImage(ctx context.Context, fetcher ImageFetcher, store Storage) (string, int64, error) {
	body, contentType, err := fetcher.Fetch(ctx, wc.URL)
	if err != nil {
		return "", 0, fmt.Errorf("CaptureImage: fetch: %w", err)
	}
	defer body.Close()

	key := wc.targetKey() + extensionForContentType(contentType)

	counter := &countingReader{r: body}
	if err := store.Write(ctx, key, counter); err != nil {
		return "", 0, fmt.Errorf("CaptureImage: store: %w", err)
	}
	return key, counter.n, nil
}

// targetKey builds the storage key for the current scheduled capture:
// "{FolderPath}/{Name} YYYYMMDDhhmmss"
func (wc *Webcam) targetKey() string {
	wc.mu.RLock()
	t := wc.CaptureTimes[wc.NextCapture]
	wc.mu.RUnlock()
	return wc.FolderPath + "/" + wc.Name + " " + t.UTC().Format("20060102150405")
}

// AdjustBackoff implements the exponential backoff policy:
// start at 1 s, double on each failure, cap at 10 min.
func (wc *Webcam) AdjustBackoff() {
	wc.mu.Lock()
	defer wc.mu.Unlock()

	if wc.Backoff == 0 {
		wc.Backoff = backoffInitial
	} else {
		wc.Backoff *= 2
	}
	if wc.Backoff > backoffMax {
		wc.Backoff = backoffMax
	}
}

// IsTimeForCapture returns true if the current wall-clock time is at or past
// the next scheduled capture time.
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

// UpdateNextCapture advances NextCapture to the first future time in CaptureTimes.
// If all times have passed it computes tomorrow's schedule, retrying up to 3 times.
// "Tomorrow" is computed in the webcam's local timezone to stay correct when the
// server runs in a different timezone.
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

	// Compute tomorrow noon in the webcam's timezone (noon avoids DST edge cases).
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
