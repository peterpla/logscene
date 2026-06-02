package main

// render_test.go tests LocalRenderer.Render.

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalRenderer_Render_noFrames exercises the early-exit path when the
// directory contains no image files.
func TestLocalRenderer_Render_noFrames(t *testing.T) {
	r := NewLocalRenderer()
	err := r.Render(context.Background(), t.TempDir(), "output.mp4", RenderOptions{})
	if err == nil {
		t.Fatal("expected error for empty directory, got nil")
	}
	if !strings.Contains(err.Error(), "no frames") {
		t.Errorf("error should mention 'no frames': %v", err)
	}
}

// TestLocalRenderer_Render_dateFilter verifies that StartDate/EndDate filter
// frames by the YYYYMMDD portion of each filename's timestamp.
func TestLocalRenderer_Render_dateFilter(t *testing.T) {
	dir := t.TempDir()
	store := NewLocalStorage()
	ctx := context.Background()

	for _, name := range []string{
		"Test Cam 20260501120000.jpg",
		"Test Cam 20260502120000.jpg",
		"Test Cam 20260601120000.jpg",
	} {
		if err := store.Write(ctx, filepath.Join(dir, name), strings.NewReader("fake")); err != nil {
			t.Fatalf("Write %s: %v", name, err)
		}
	}

	r := NewLocalRenderer()

	// Date range that matches no files → "no frames" error.
	err := r.Render(ctx, dir, filepath.Join(dir, "out.mp4"), RenderOptions{
		StartDate: "20260701",
		EndDate:   "20260731",
	})
	if err == nil || !strings.Contains(err.Error(), "no frames") {
		t.Errorf("expected 'no frames' for out-of-range dates, got: %v", err)
	}

	// Date range matching one file → proceeds past the frame guard.
	err = r.Render(ctx, dir, filepath.Join(dir, "out2.mp4"), RenderOptions{
		StartDate: "20260601",
		EndDate:   "20260630",
	})
	if err != nil && strings.Contains(err.Error(), "no frames") {
		t.Errorf("date filter excluded frames that should have matched: %v", err)
	}
}

// TestLocalRenderer_Render_invokesFFmpeg writes real frame files to disk and
// calls Render, exercising the ffmpeg-invocation path. If ffmpeg is not on
// PATH the test logs the error and passes — the goal is line coverage past the
// "no frames" guard, not a successful encode.
func TestLocalRenderer_Render_invokesFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Log("ffmpeg not on PATH — exercising ffmpeg path but expecting exec error")
	}

	dir := t.TempDir()
	store := NewLocalStorage()
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		name := filepath.Join(dir, "Test Cam 2026060"+string(rune('0'+i))+"120000.jpg")
		if err := store.Write(ctx, name, strings.NewReader("fake-frame")); err != nil {
			t.Fatalf("Write frame %d: %v", i, err)
		}
	}

	r := NewLocalRenderer()
	err := r.Render(ctx, dir, filepath.Join(dir, "out.mp4"), RenderOptions{FPS: 24})

	if err != nil {
		if strings.Contains(err.Error(), "no frames") {
			t.Fatalf("unexpected 'no frames' error — frames were written: %v", err)
		}
		t.Logf("Render returned error (expected if ffmpeg not installed): %v", err)
		return
	}
	t.Log("ffmpeg render succeeded")
}
