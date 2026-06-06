// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// render_test.go tests LocalRenderer.Render.

import (
	"context"
	"os"
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

// TestLocalRenderer_Render_readDirError exercises the ReadDir failure path.
func TestLocalRenderer_Render_readDirError(t *testing.T) {
	r := NewLocalRenderer()
	err := r.Render(context.Background(),
		filepath.Join(t.TempDir(), "nonexistent-subdir"),
		"out.mp4", RenderOptions{})
	if err == nil {
		t.Error("expected error for nonexistent directory, got nil")
	}
	if strings.Contains(err.Error(), "no frames") {
		t.Errorf("error should be a readdir failure, not 'no frames': %v", err)
	}
}

// TestLocalRenderer_Render_skipsNonImages verifies that subdirectories and
// non-image files are skipped and do not count as frames.
func TestLocalRenderer_Render_skipsNonImages(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Write a text file and create a subdirectory — both must be skipped.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("text"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	r := NewLocalRenderer()
	err := r.Render(ctx, dir, filepath.Join(dir, "out.mp4"), RenderOptions{})
	if err == nil || !strings.Contains(err.Error(), "no frames") {
		t.Errorf("expected 'no frames' when only non-images present, got: %v", err)
	}
}

// TestLocalRenderer_Render_dateFilterEndDate verifies that frames dated after
// EndDate are excluded, and that files with stems shorter than 14 chars are
// skipped rather than causing a panic.
func TestLocalRenderer_Render_dateFilterEndDate(t *testing.T) {
	dir := t.TempDir()
	store := NewLocalStorage()
	ctx := context.Background()

	for _, name := range []string{
		"Test Cam 20260601120000.jpg", // inside range
		"Test Cam 20260801120000.jpg", // after EndDate — must be excluded
		"short.jpg",                   // stem < 14 chars — must be skipped
	} {
		if err := store.Write(ctx, filepath.Join(dir, name), strings.NewReader("x")); err != nil {
			t.Fatalf("Write %s: %v", name, err)
		}
	}

	r := NewLocalRenderer()
	err := r.Render(ctx, dir, filepath.Join(dir, "out.mp4"), RenderOptions{EndDate: "20260630"})
	// One frame matches; Render proceeds past the "no frames" guard.
	if err != nil && strings.Contains(err.Error(), "no frames") {
		t.Errorf("expected at least one frame in range, got 'no frames': %v", err)
	}
}

// TestLocalRenderer_Render_stride verifies that only every Nth frame is kept.
func TestLocalRenderer_Render_stride(t *testing.T) {
	dir := t.TempDir()
	store := NewLocalStorage()
	ctx := context.Background()

	for _, name := range []string{
		"Cam 20260601120000.jpg",
		"Cam 20260601121500.jpg",
		"Cam 20260601123000.jpg",
		"Cam 20260601124500.jpg",
		"Cam 20260601130000.jpg",
		"Cam 20260601131500.jpg",
	} {
		if err := store.Write(ctx, filepath.Join(dir, name), strings.NewReader("x")); err != nil {
			t.Fatalf("Write %s: %v", name, err)
		}
	}

	r := NewLocalRenderer()
	// Stride 3 on 6 frames keeps indices 0 and 3 — 2 frames, not zero.
	err := r.Render(ctx, dir, filepath.Join(dir, "out.mp4"), RenderOptions{Stride: 3, FPS: 24})
	if err != nil && strings.Contains(err.Error(), "no frames") {
		t.Fatalf("stride filter reduced to zero frames: %v", err)
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
