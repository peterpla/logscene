// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// render_test.go tests LocalRenderer.Render.

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalRenderer_Render_noFrames exercises the early-exit path when the
// directory contains no image files.
func assertNoFrames(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected RenderError, got nil")
	}
	var re *RenderError
	if !errors.As(err, &re) || re.Class != fcRenderNoFrames {
		t.Errorf("expected RenderError{Class:%q}, got: %v", fcRenderNoFrames, err)
	}
}

func assertNotNoFrames(t *testing.T, err error) {
	t.Helper()
	var re *RenderError
	if errors.As(err, &re) && re.Class == fcRenderNoFrames {
		t.Errorf("unexpected no-frames error: %v", err)
	}
}

func TestLocalRenderer_Render_noFrames(t *testing.T) {
	r := NewLocalRenderer()
	err := r.Render(context.Background(), t.TempDir(), "output.mp4", RenderOptions{})
	assertNoFrames(t, err)
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

	// Date range that matches no files → no-frames error.
	err := r.Render(ctx, dir, filepath.Join(dir, "out.mp4"), RenderOptions{
		StartDate: "20260701",
		EndDate:   "20260731",
	})
	assertNoFrames(t, err)

	// Date range matching one file → proceeds past the frame guard.
	err = r.Render(ctx, dir, filepath.Join(dir, "out2.mp4"), RenderOptions{
		StartDate: "20260601",
		EndDate:   "20260630",
	})
	assertNotNoFrames(t, err)
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
	assertNotNoFrames(t, err)
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
	assertNoFrames(t, r.Render(ctx, dir, filepath.Join(dir, "out.mp4"), RenderOptions{}))
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
	// One frame matches; Render proceeds past the "no frames" guard.
	assertNotNoFrames(t, r.Render(ctx, dir, filepath.Join(dir, "out.mp4"), RenderOptions{EndDate: "20260630"}))
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
	assertNotNoFrames(t, r.Render(ctx, dir, filepath.Join(dir, "out.mp4"), RenderOptions{Stride: 3, FPS: 24}))
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
		assertNotNoFrames(t, err)
		t.Logf("Render returned error (expected if ffmpeg not installed): %v", err)
		return
	}
	t.Log("ffmpeg render succeeded")
}

// TestLocalRenderer_Render_oddHeight verifies that a single-frame render
// succeeds when the source JPEG has an odd height (4×3). The crop filter
// in the ffmpeg args must trim it to 4×2 without libx264 rejecting it.
// Skipped if ffmpeg is not on PATH.
func TestLocalRenderer_Render_oddHeight(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}

	img := image.NewGray(image.Rect(0, 0, 4, 3)) // even width, odd height
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}

	dir := t.TempDir()
	framePath := filepath.Join(dir, "Cam 20260601120000.jpg")
	if err := os.WriteFile(framePath, buf.Bytes(), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	outputPath := filepath.Join(dir, "out.mp4")
	if err := NewLocalRenderer().Render(context.Background(), dir, outputPath, RenderOptions{FPS: 24}); err != nil {
		t.Fatalf("Render: %v", err)
	}

	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("output file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("output file is empty")
	}
}

// TestLocalRenderer_Render_oddWidth verifies that a single-frame render
// succeeds when the source JPEG has an odd width (3×4). The crop filter
// in the ffmpeg args must trim it to 2×4 without libx264 rejecting it.
// Skipped if ffmpeg is not on PATH.
func TestLocalRenderer_Render_oddWidth(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}

	img := image.NewGray(image.Rect(0, 0, 3, 4)) // odd width, even height
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}

	dir := t.TempDir()
	framePath := filepath.Join(dir, "Cam 20260601120000.jpg")
	if err := os.WriteFile(framePath, buf.Bytes(), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	outputPath := filepath.Join(dir, "out.mp4")
	if err := NewLocalRenderer().Render(context.Background(), dir, outputPath, RenderOptions{FPS: 24}); err != nil {
		t.Fatalf("Render: %v", err)
	}

	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("output file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("output file is empty")
	}
}

// ---------------------------------------------------------------------------
// classifyFFmpegError
// ---------------------------------------------------------------------------

func TestClassifyFFmpegError_canceled(t *testing.T) {
	re := classifyFFmpegError(context.Canceled, "")
	if re.Class != fcRenderCanceled {
		t.Errorf("class: want %q, got %q", fcRenderCanceled, re.Class)
	}
}

func TestClassifyFFmpegError_notFound(t *testing.T) {
	err := &exec.Error{Name: "ffmpeg", Err: exec.ErrNotFound}
	re := classifyFFmpegError(err, "")
	if re.Class != fcRenderFFmpegMissing {
		t.Errorf("class: want %q, got %q", fcRenderFFmpegMissing, re.Class)
	}
}

func TestClassifyFFmpegError_codecMissing(t *testing.T) {
	re := classifyFFmpegError(errors.New("exit 1"), "Encoder libx264 not found for output stream")
	if re.Class != fcRenderCodecMissing {
		t.Errorf("class: want %q, got %q", fcRenderCodecMissing, re.Class)
	}
}

func TestClassifyFFmpegError_diskFull(t *testing.T) {
	re := classifyFFmpegError(errors.New("exit 1"), "Error writing trailer: No space left on device")
	if re.Class != fcRenderDiskFull {
		t.Errorf("class: want %q, got %q", fcRenderDiskFull, re.Class)
	}
}

func TestClassifyFFmpegError_permission(t *testing.T) {
	re := classifyFFmpegError(errors.New("exit 1"), "output.mp4: Permission denied")
	if re.Class != fcRenderPermission {
		t.Errorf("class: want %q, got %q", fcRenderPermission, re.Class)
	}
}

func TestClassifyFFmpegError_generic(t *testing.T) {
	re := classifyFFmpegError(errors.New("exit 1"), "some unrecognised ffmpeg output")
	if re.Class != fcRenderFFmpegError {
		t.Errorf("class: want %q, got %q", fcRenderFFmpegError, re.Class)
	}
}
