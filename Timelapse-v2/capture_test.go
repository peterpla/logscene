package main

// capture_test.go tests CaptureImage, AdjustBackoff, IsTimeForCapture,
// and UpdateNextCapture.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

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
// AdjustBackoff
// ---------------------------------------------------------------------------

func TestAdjustBackoff_startsAtOneSecond(t *testing.T) {
	wc := newWebcam()
	wc.Backoff = 0
	wc.AdjustBackoff()
	if wc.Backoff != backoffInitial {
		t.Errorf("want %v, got %v", backoffInitial, wc.Backoff)
	}
}

func TestAdjustBackoff_doubles(t *testing.T) {
	wc := newWebcam()
	wc.Backoff = 0
	for i := 0; i < 5; i++ {
		wc.AdjustBackoff()
	}
	want := backoffInitial * (1 << 4) // 1s, 2s, 4s, 8s, 16s
	if wc.Backoff != want {
		t.Errorf("after 5 calls: want %v, got %v", want, wc.Backoff)
	}
}

func TestAdjustBackoff_capsAtMax(t *testing.T) {
	wc := newWebcam()
	wc.Backoff = backoffMax / 2
	wc.AdjustBackoff()
	if wc.Backoff != backoffMax {
		t.Errorf("want max %v, got %v", backoffMax, wc.Backoff)
	}
	wc.AdjustBackoff()
	if wc.Backoff != backoffMax {
		t.Errorf("should stay at max %v, got %v", backoffMax, wc.Backoff)
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
	fetcher := &mockImageFetcher{
		data:        []byte("fake-jpeg-data"),
		contentType: "image/jpeg",
	}

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 0)
	wc.CaptureTimes = []time.Time{time.Date(2026, 6, 1, 13, 0, 0, 0, time.UTC)}
	wc.NextCapture = 0

	key, size, err := wc.CaptureImage(context.Background(), fetcher, store)
	if err != nil {
		t.Fatalf("CaptureImage: %v", err)
	}
	if size != int64(len(fetcher.data)) {
		t.Errorf("size: want %d, got %d", len(fetcher.data), size)
	}
	if !strings.HasSuffix(key, ".jpg") {
		t.Errorf("key %q should end in .jpg", key)
	}
	keys, _ := store.List(context.Background(), wc.FolderPath)
	if len(keys) != 1 {
		t.Errorf("expected 1 stored key, got %d: %v", len(keys), keys)
	}
}

func TestCaptureImage_fetchError(t *testing.T) {
	store := testStorage(t)
	fetcher := &mockImageFetcher{err: errors.New("connection refused")}

	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 0)
	wc.CaptureTimes = []time.Time{time.Now()}
	wc.NextCapture = 0

	_, _, err := wc.CaptureImage(context.Background(), fetcher, store)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	keys, _ := store.List(context.Background(), wc.FolderPath)
	if len(keys) != 0 {
		t.Errorf("expected 0 stored keys on fetch error, got %d", len(keys))
	}
}

func TestCaptureImage_keyUsesScheduledTime(t *testing.T) {
	store := testStorage(t)
	fetcher := &mockImageFetcher{data: []byte("img"), contentType: "image/png"}

	scheduled := time.Date(2026, 6, 1, 14, 30, 0, 0, time.UTC)
	wc := testWebcam(t, flagFirstSunrise, flagLastSunset, 0)
	wc.Name = "My Cam"
	wc.CaptureTimes = []time.Time{scheduled}
	wc.NextCapture = 0

	key, _, err := wc.CaptureImage(context.Background(), fetcher, store)
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
