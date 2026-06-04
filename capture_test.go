// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// capture_test.go tests CaptureImage, the graduated outage-backoff methods
// (recordFailure, recordSuccess, shouldAttemptNow, autoSuspendDue),
// IsTimeForCapture, and UpdateNextCapture.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// failingNTimesSolarClient returns an error for the first n calls, then succeeds.
type failingNTimesSolarClient struct {
	n     int
	calls int
	times SolarTimes
}

func (f *failingNTimesSolarClient) GetSolarTimes(_ context.Context, _, _ float64, _ time.Time) (SolarTimes, error) {
	f.calls++
	if f.calls <= f.n {
		return SolarTimes{}, fmt.Errorf("solar unavailable (call %d)", f.calls)
	}
	return f.times, nil
}

// ---------------------------------------------------------------------------
// extensionForContentType
// ---------------------------------------------------------------------------

func TestExtensionForContentType(t *testing.T) {
	cases := []struct {
		ct   string
		want string
	}{
		{"image/jpeg", ".jpg"},
		{"image/jpeg; charset=utf-8", ".jpg"},
		{"image/png", ".png"},
		{"image/gif", ".gif"},
		{"image/webp", ".webp"},
		{"text/html", ".jpg"},
		{"", ".jpg"},
	}
	for _, c := range cases {
		got := extensionForContentType(c.ct)
		if got != c.want {
			t.Errorf("extensionForContentType(%q) = %q, want %q", c.ct, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// recordFailure / recordSuccess
// ---------------------------------------------------------------------------

func TestRecordFailure_setsFirstFailure(t *testing.T) {
	wc := newWebcam()
	before := time.Now()
	wc.recordFailure()
	after := time.Now()

	if wc.FirstFailure.IsZero() {
		t.Fatal("FirstFailure should be set after first failure")
	}
	if wc.FirstFailure.Before(before) || wc.FirstFailure.After(after) {
		t.Errorf("FirstFailure %v outside [%v, %v]", wc.FirstFailure, before, after)
	}
	if wc.Backoff != backoffInitial {
		t.Errorf("first failure: want Backoff=%v, got %v", backoffInitial, wc.Backoff)
	}
}

func TestRecordFailure_doublesBackoffInTier1(t *testing.T) {
	wc := newWebcam()
	wc.recordFailure() // Backoff = 1s
	wc.recordFailure() // Backoff = 2s
	wc.recordFailure() // Backoff = 4s
	want := backoffInitial * 4
	if wc.Backoff != want {
		t.Errorf("after 3 failures: want %v, got %v", want, wc.Backoff)
	}
}

func TestRecordFailure_capsBackoffAtMax(t *testing.T) {
	wc := newWebcam()
	wc.FirstFailure = time.Now() // within tier 1
	wc.Backoff = backoffMax / 2
	wc.recordFailure()
	if wc.Backoff != backoffMax {
		t.Errorf("want Backoff=%v, got %v", backoffMax, wc.Backoff)
	}
	wc.recordFailure()
	if wc.Backoff != backoffMax {
		t.Errorf("Backoff should stay at max, got %v", wc.Backoff)
	}
}

func TestRecordFailure_doesNotChangeBackoffInTier2(t *testing.T) {
	wc := newWebcam()
	wc.FirstFailure = time.Now().Add(-30 * time.Hour) // tier 2
	wc.Backoff = backoffMax
	wc.recordFailure()
	if wc.Backoff != backoffMax {
		t.Errorf("Backoff should not change in tier 2, got %v", wc.Backoff)
	}
}

func TestRecordSuccess_resetsAllFields(t *testing.T) {
	wc := newWebcam()
	wc.FirstFailure = time.Now().Add(-time.Hour)
	wc.LastAttempt = time.Now()
	wc.Backoff = 5 * time.Second

	wc.recordSuccess()

	if !wc.FirstFailure.IsZero() {
		t.Error("FirstFailure should be zero after success")
	}
	if !wc.LastAttempt.IsZero() {
		t.Error("LastAttempt should be zero after success")
	}
	if wc.Backoff != 0 {
		t.Errorf("Backoff should be 0 after success, got %v", wc.Backoff)
	}
}

// ---------------------------------------------------------------------------
// shouldAttemptNow
// ---------------------------------------------------------------------------

func scheduledInPast(wc *Webcam) {
	wc.CaptureTimes = []time.Time{time.Now().Add(-time.Minute)}
	wc.NextCapture = 0
}

func TestShouldAttemptNow_noFailureStreak(t *testing.T) {
	wc := newWebcam()
	scheduledInPast(wc)
	if !wc.shouldAttemptNow() {
		t.Error("expected true with no failure streak")
	}
}

func TestShouldAttemptNow_captureNotYetDue(t *testing.T) {
	wc := newWebcam()
	wc.CaptureTimes = []time.Time{time.Now().Add(time.Hour)}
	wc.NextCapture = 0
	if wc.shouldAttemptNow() {
		t.Error("expected false when capture is in the future")
	}
}

func TestShouldAttemptNow_tier1BackoffElapsed(t *testing.T) {
	wc := newWebcam()
	scheduledInPast(wc)
	wc.FirstFailure = time.Now().Add(-time.Hour) // tier 1
	wc.Backoff = 5 * time.Second
	wc.LastAttempt = time.Now().Add(-10 * time.Second) // 10s > 5s backoff
	if !wc.shouldAttemptNow() {
		t.Error("expected true when tier-1 backoff has elapsed")
	}
}

func TestShouldAttemptNow_tier1BackoffNotElapsed(t *testing.T) {
	wc := newWebcam()
	scheduledInPast(wc)
	wc.FirstFailure = time.Now().Add(-time.Hour) // tier 1
	wc.Backoff = 10 * time.Minute
	wc.LastAttempt = time.Now().Add(-5 * time.Minute) // 5m < 10m backoff
	if wc.shouldAttemptNow() {
		t.Error("expected false when tier-1 backoff has not elapsed")
	}
}

func TestShouldAttemptNow_tier2Ready(t *testing.T) {
	wc := newWebcam()
	scheduledInPast(wc)
	wc.FirstFailure = time.Now().Add(-30 * time.Hour) // tier 2
	wc.LastAttempt = time.Now().Add(-90 * time.Minute) // 90m > 1h
	if !wc.shouldAttemptNow() {
		t.Error("expected true when tier-2 hour interval has elapsed")
	}
}

func TestShouldAttemptNow_tier2NotReady(t *testing.T) {
	wc := newWebcam()
	scheduledInPast(wc)
	wc.FirstFailure = time.Now().Add(-30 * time.Hour) // tier 2
	wc.LastAttempt = time.Now().Add(-30 * time.Minute) // 30m < 1h
	if wc.shouldAttemptNow() {
		t.Error("expected false when tier-2 hour interval has not elapsed")
	}
}

func TestShouldAttemptNow_tier3Ready(t *testing.T) {
	wc := newWebcam()
	scheduledInPast(wc)
	wc.FirstFailure = time.Now().Add(-5 * 24 * time.Hour) // tier 3
	wc.LastAttempt = time.Now().Add(-25 * time.Hour) // 25h > 24h
	if !wc.shouldAttemptNow() {
		t.Error("expected true when tier-3 day interval has elapsed")
	}
}

func TestShouldAttemptNow_tier3NotReady(t *testing.T) {
	wc := newWebcam()
	scheduledInPast(wc)
	wc.FirstFailure = time.Now().Add(-5 * 24 * time.Hour) // tier 3
	wc.LastAttempt = time.Now().Add(-12 * time.Hour) // 12h < 24h
	if wc.shouldAttemptNow() {
		t.Error("expected false when tier-3 day interval has not elapsed")
	}
}

// ---------------------------------------------------------------------------
// autoSuspendDue
// ---------------------------------------------------------------------------

func TestAutoSuspendDue_noStreak(t *testing.T) {
	wc := newWebcam()
	if wc.autoSuspendDue() {
		t.Error("expected false with no failure streak")
	}
}

func TestAutoSuspendDue_belowThreshold(t *testing.T) {
	wc := newWebcam()
	wc.FirstFailure = time.Now().Add(-13 * 24 * time.Hour) // 13 days
	if wc.autoSuspendDue() {
		t.Error("expected false at 13 days (threshold is 14)")
	}
}

func TestAutoSuspendDue_atThreshold(t *testing.T) {
	wc := newWebcam()
	wc.FirstFailure = time.Now().Add(-15 * 24 * time.Hour) // 15 days
	if !wc.autoSuspendDue() {
		t.Error("expected true at 15 days (threshold is 14)")
	}
}

// ---------------------------------------------------------------------------
// IsTimeForCapture
// ---------------------------------------------------------------------------

func TestIsTimeForCapture_notYet(t *testing.T) {
	wc := newWebcam()
	wc.CaptureTimes = []time.Time{time.Now().Add(time.Hour)}
	wc.NextCapture = 0
	if wc.IsTimeForCapture() {
		t.Error("expected false when capture time is in the future")
	}
}

func TestIsTimeForCapture_past(t *testing.T) {
	wc := newWebcam()
	wc.CaptureTimes = []time.Time{time.Now().Add(-time.Second)}
	wc.NextCapture = 0
	if !wc.IsTimeForCapture() {
		t.Error("expected true when capture time is in the past")
	}
}

func TestIsTimeForCapture_emptySchedule(t *testing.T) {
	wc := newWebcam()
	wc.CaptureTimes = []time.Time{}
	wc.NextCapture = 0
	if wc.IsTimeForCapture() {
		t.Error("expected false when schedule is empty")
	}
}

// ---------------------------------------------------------------------------
// CaptureImage
// ---------------------------------------------------------------------------

func TestCaptureImage_success(t *testing.T) {
	store := testStorage(t)
	baseDir := t.TempDir()
	fetcher := &mockImageFetcher{
		data:        []byte("fake-jpeg-data"),
		contentType: "image/jpeg",
	}

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 0)
	wc.CaptureTimes = []time.Time{time.Date(2026, 6, 1, 13, 0, 0, 0, time.UTC)}
	wc.NextCapture = 0

	key, size, err := wc.CaptureImage(context.Background(), fetcher, store, baseDir)
	if err != nil {
		t.Fatalf("CaptureImage: %v", err)
	}
	if size != int64(len(fetcher.data)) {
		t.Errorf("size: want %d, got %d", len(fetcher.data), size)
	}
	if !strings.HasSuffix(key, ".jpg") {
		t.Errorf("key %q should end in .jpg", key)
	}
	keys, _ := store.List(context.Background(), baseDir+"/"+wc.Folder)
	if len(keys) != 1 {
		t.Errorf("expected 1 stored key, got %d: %v", len(keys), keys)
	}
}

func TestCaptureImage_fetchError(t *testing.T) {
	store := testStorage(t)
	baseDir := t.TempDir()
	fetcher := &mockImageFetcher{err: errors.New("connection refused")}

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 0)
	wc.CaptureTimes = []time.Time{time.Now()}
	wc.NextCapture = 0

	_, _, err := wc.CaptureImage(context.Background(), fetcher, store, baseDir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	keys, _ := store.List(context.Background(), baseDir+"/"+wc.Folder)
	if len(keys) != 0 {
		t.Errorf("expected 0 stored keys on fetch error, got %d", len(keys))
	}
}

func TestCaptureImage_keyUsesScheduledTime(t *testing.T) {
	store := testStorage(t)
	baseDir := t.TempDir()
	fetcher := &mockImageFetcher{data: []byte("img"), contentType: "image/png"}

	scheduled := time.Date(2026, 6, 1, 14, 30, 0, 0, time.UTC)
	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 0)
	wc.Name = "My Cam"
	wc.CaptureTimes = []time.Time{scheduled}
	wc.NextCapture = 0

	key, _, err := wc.CaptureImage(context.Background(), fetcher, store, baseDir)
	if err != nil {
		t.Fatalf("CaptureImage: %v", err)
	}
	wantFragment := "20260601143000"
	if !strings.Contains(key, wantFragment) {
		t.Errorf("key %q does not contain scheduled timestamp %s", key, wantFragment)
	}
}

// ---------------------------------------------------------------------------
// UpdateNextCapture
// ---------------------------------------------------------------------------

func TestUpdateNextCapture_advancesIndex(t *testing.T) {
	tzClient := &fixedTimezoneClient{tz: "America/Los_Angeles"}
	solar := &fixedSolarClient{times: laFixedSolar()}

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 1)
	now := time.Now()
	wc.CaptureTimes = []time.Time{
		now.Add(-2 * time.Hour),
		now.Add(-1 * time.Hour),
		now.Add(1 * time.Hour),
	}
	wc.NextCapture = 0

	if err := wc.UpdateNextCapture(context.Background(), now, tzClient, solar); err != nil {
		t.Fatalf("UpdateNextCapture: %v", err)
	}
	wc.mu.RLock()
	got := wc.NextCapture
	wc.mu.RUnlock()
	if got != 2 {
		t.Errorf("NextCapture: want 2, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// nextRetryInterval (public wrapper around currentRetryInterval)
// ---------------------------------------------------------------------------

func TestNextRetryInterval_noStreak(t *testing.T) {
	wc := newWebcam()
	if d := wc.nextRetryInterval(); d != 0 {
		t.Errorf("want 0 with no failure streak, got %v", d)
	}
}

func TestNextRetryInterval_tier1(t *testing.T) {
	wc := newWebcam()
	wc.FirstFailure = time.Now()
	wc.Backoff = 5 * time.Second
	if d := wc.nextRetryInterval(); d != 5*time.Second {
		t.Errorf("want 5s in tier 1, got %v", d)
	}
}

func TestUpdateNextCapture_contextCancelledDuringRetry(t *testing.T) {
	tzClient := &fixedTimezoneClient{tz: "America/Los_Angeles"}
	// Solar client always fails, so every SetCaptureTimes attempt returns an error.
	solar := &fixedSolarClient{err: errors.New("solar unavailable")}

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 0)
	wc.CaptureTimes = []time.Time{
		time.Now().Add(-2 * time.Hour),
		time.Now().Add(-1 * time.Hour),
	}
	loc, _ := time.LoadLocation("America/Los_Angeles")
	wc.mu.Lock()
	wc.WebcamLoc = loc
	wc.WebcamTZ = "America/Los_Angeles"
	wc.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel the context a short time after the first SetCaptureTimes fails,
	// before the 5-second retry timer fires.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	err := wc.UpdateNextCapture(ctx, time.Now(), tzClient, solar)
	if err == nil {
		t.Fatal("expected error after context cancellation, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// captureViaFfmpeg — lavfi synthetic source (skipped if ffmpeg absent)
// ---------------------------------------------------------------------------

func TestCaptureViaFfmpeg_lavfi(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	store := NewMemStorage()
	key := "test/output.jpg"
	// lavfi testsrc generates a synthetic video frame with no hardware required.
	args := []string{"-f", "lavfi", "-i", "testsrc=duration=1:size=10x10"}
	size, err := captureViaFfmpeg(context.Background(), args, store, key)
	if err != nil {
		t.Fatalf("captureViaFfmpeg: %v", err)
	}
	if size == 0 {
		t.Error("expected non-zero output size")
	}
	rc, err := store.Read(context.Background(), key)
	if err != nil {
		t.Fatalf("Read after capture: %v", err)
	}
	rc.Close()
}

func TestUpdateNextCapture_rollsOverToTomorrow(t *testing.T) {
	tzClient := &fixedTimezoneClient{tz: "America/Los_Angeles"}
	solar := &fixedSolarClient{times: laFixedSolar()}

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 0)
	wc.CaptureTimes = []time.Time{
		time.Now().Add(-2 * time.Hour),
		time.Now().Add(-1 * time.Hour),
	}
	wc.NextCapture = 0

	loc, _ := time.LoadLocation("America/Los_Angeles")
	wc.mu.Lock()
	wc.WebcamLoc = loc
	wc.WebcamTZ = "America/Los_Angeles"
	wc.mu.Unlock()

	if err := wc.UpdateNextCapture(context.Background(), time.Now(), tzClient, solar); err != nil {
		t.Fatalf("UpdateNextCapture: %v", err)
	}
	wc.mu.RLock()
	idx := wc.NextCapture
	ct := wc.CaptureTimes
	wc.mu.RUnlock()

	if idx != 0 {
		t.Errorf("NextCapture after rollover: want 0, got %d", idx)
	}
	if len(ct) < 2 {
		t.Errorf("CaptureTimes after rollover: want ≥2, got %d", len(ct))
	}
}

// ---------------------------------------------------------------------------
// UpdateNextCapture — unsorted CaptureTimes triggers sort branch
// ---------------------------------------------------------------------------

func TestUpdateNextCapture_sortsUnsortedCaptureTimes(t *testing.T) {
	tzClient := &fixedTimezoneClient{tz: "America/Los_Angeles"}
	solar := &fixedSolarClient{times: laFixedSolar()}

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 0)
	now := time.Now()
	// Deliberately reverse order — the sort branch must execute.
	wc.CaptureTimes = []time.Time{
		now.Add(2 * time.Hour),
		now.Add(1 * time.Hour),
		now.Add(-1 * time.Hour),
	}
	wc.NextCapture = 0

	if err := wc.UpdateNextCapture(context.Background(), now, tzClient, solar); err != nil {
		t.Fatalf("UpdateNextCapture: %v", err)
	}
	wc.mu.RLock()
	ct := wc.CaptureTimes
	wc.mu.RUnlock()
	// Ascending order after sort: each element before the next.
	for i := 1; i < len(ct); i++ {
		if !ct[i-1].Before(ct[i]) {
			t.Errorf("CaptureTimes not sorted at index %d", i)
		}
	}
}

// ---------------------------------------------------------------------------
// shouldAttemptNow — NextCapture out-of-bounds
// ---------------------------------------------------------------------------

func TestShouldAttemptNow_nextCaptureOutOfBounds(t *testing.T) {
	wc := newWebcam()
	wc.CaptureTimes = []time.Time{time.Now().Add(-time.Minute)}
	wc.NextCapture = 99 // past end of slice
	if wc.shouldAttemptNow() {
		t.Error("expected false when NextCapture is out of bounds")
	}
}

// ---------------------------------------------------------------------------
// captureViaFfmpeg — ffmpeg exits non-zero
// ---------------------------------------------------------------------------

func TestCaptureViaFfmpeg_ffmpegFailure(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	store := NewMemStorage()
	// Deliberately invalid format flag — ffmpeg exits non-zero immediately.
	args := []string{"-f", "nonexistent_format_xyz", "-i", "dummy"}
	_, err := captureViaFfmpeg(context.Background(), args, store, "test/out.jpg")
	if err == nil {
		t.Error("expected error when ffmpeg fails, got nil")
	}
}

// ---------------------------------------------------------------------------
// CaptureImage — store.Write error in url branch
// ---------------------------------------------------------------------------

func TestCaptureImage_storeWriteError(t *testing.T) {
	store := &failWriteStorage{MemStorage: NewMemStorage()}
	baseDir := t.TempDir()
	fetcher := &mockImageFetcher{data: []byte("img"), contentType: "image/jpeg"}

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 0)
	wc.CaptureTimes = []time.Time{time.Now()}
	wc.NextCapture = 0

	_, _, err := wc.CaptureImage(context.Background(), fetcher, store, baseDir)
	if err == nil {
		t.Error("expected error when storage write fails, got nil")
	}
}

// ---------------------------------------------------------------------------
// UpdateNextCapture — retry warning log (transient solar failure)
// ---------------------------------------------------------------------------

func TestUpdateNextCapture_retriesOnTransientSolarFailure(t *testing.T) {
	tzClient := &fixedTimezoneClient{tz: "America/Los_Angeles"}
	// Fails once, then succeeds — exercises the retry log path.
	solar := &failingNTimesSolarClient{n: 1, times: laFixedSolar()}

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 0)
	wc.CaptureTimes = []time.Time{
		time.Now().Add(-2 * time.Hour),
		time.Now().Add(-1 * time.Hour),
	}
	loc, _ := time.LoadLocation("America/Los_Angeles")
	wc.mu.Lock()
	wc.WebcamLoc = loc
	wc.WebcamTZ = "America/Los_Angeles"
	wc.mu.Unlock()

	if err := wc.UpdateNextCapture(context.Background(), time.Now(), tzClient, solar); err != nil {
		t.Fatalf("UpdateNextCapture: %v", err)
	}
	if solar.calls < 2 {
		t.Errorf("expected ≥2 solar calls (1 fail + 1 success), got %d", solar.calls)
	}
}

// ---------------------------------------------------------------------------
// CaptureImage — stream dispatch
// ---------------------------------------------------------------------------

func TestCaptureImage_stream(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH")
	}

	// Create a minimal JPEG using lavfi for ffmpeg to read as a stream input.
	tmpJPEG := t.TempDir() + "/src.jpg"
	setup := exec.Command("ffmpeg",
		"-f", "lavfi", "-i", "testsrc=duration=1:size=10x10",
		"-frames:v", "1", "-y", tmpJPEG)
	if out, err := setup.CombinedOutput(); err != nil {
		t.Skipf("lavfi setup failed: %v: %s", err, out)
	}

	store := NewMemStorage()
	baseDir := t.TempDir()

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 0)
	wc.SourceType = "stream"
	wc.URL = tmpJPEG
	wc.CaptureTimes = []time.Time{time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)}
	wc.NextCapture = 0

	key, size, err := wc.CaptureImage(context.Background(), nil, store, baseDir)
	if err != nil {
		t.Fatalf("CaptureImage stream: %v", err)
	}
	if size == 0 {
		t.Error("expected non-zero capture size")
	}
	if !strings.HasSuffix(key, ".jpg") {
		t.Errorf("key %q should end in .jpg", key)
	}
}
