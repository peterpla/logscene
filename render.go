// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// render.go defines the Renderer interface and LocalRenderer, which invokes
// ffmpeg to assemble captured frames into a timelapse video.
//
// The concat demuxer is used instead of -pattern_type glob because glob is
// not supported by ffmpeg on Windows.

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// RenderOptions controls optional rendering parameters.
type RenderOptions struct {
	FPS       int    // frames per second; 0 uses the default (24)
	StartDate string // YYYYMMDD inclusive lower bound; empty means no lower bound
	EndDate   string // YYYYMMDD inclusive upper bound; empty means no upper bound
}

// Renderer assembles captured images into a timelapse video.
type Renderer interface {
	// Render collects all images in dir, optionally filtered to a date range,
	// assembles them in chronological order, and writes the resulting video
	// to outputKey.
	Render(ctx context.Context, dir, outputKey string, opts RenderOptions) error
}

// LocalRenderer uses ffmpeg to render frames stored on the local filesystem.
type LocalRenderer struct{}

// NewLocalRenderer creates a LocalRenderer.
func NewLocalRenderer() *LocalRenderer {
	return &LocalRenderer{}
}

// Render lists all .jpg/.jpeg files in dir, applies any date-range filter,
// writes a temporary concat file, and invokes ffmpeg.
//
// Filenames must end with a 14-digit UTC timestamp (YYYYMMDDhhmmss) before
// the extension — the format written by CaptureImage. Date filtering extracts
// the leading 8 digits (YYYYMMDD) from that timestamp.
//
// ffmpeg is invoked with:
//
//	ffmpeg -y -f concat -safe 0 -r <fps> -i <tmpfile> \
//	       -c:v libx264 -pix_fmt yuv420p <outputKey>
func (r *LocalRenderer) Render(ctx context.Context, dir, outputKey string, opts RenderOptions) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("Render: readdir %s: %w", dir, err)
	}

	var frames []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".jpg" && ext != ".jpeg" {
			continue
		}
		if opts.StartDate != "" || opts.EndDate != "" {
			stem := strings.TrimSuffix(name, filepath.Ext(name))
			if len(stem) < 14 {
				continue
			}
			date := stem[len(stem)-14 : len(stem)-6]
			if opts.StartDate != "" && date < opts.StartDate {
				continue
			}
			if opts.EndDate != "" && date > opts.EndDate {
				continue
			}
		}
		frames = append(frames, filepath.Join(dir, name))
	}

	if len(frames) == 0 {
		return fmt.Errorf("Render: no frames found in %q", dir)
	}
	log.Printf("Render: %d frames → %s", len(frames), outputKey)

	tmp, err := os.CreateTemp("", "timelapse-concat-*.txt")
	if err != nil {
		return fmt.Errorf("Render: create concat file: %w", err)
	}
	defer os.Remove(tmp.Name())

	for _, f := range frames {
		fmt.Fprintf(tmp, "file '%s'\n", filepath.ToSlash(f))
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("Render: close concat file: %w", err)
	}

	fps := opts.FPS
	if fps == 0 {
		fps = 24
	}

	args := []string{
		"-y",
		"-f", "concat",
		"-safe", "0",
		"-r", strconv.Itoa(fps),
		"-i", tmp.Name(),
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		outputKey,
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Render: ffmpeg: %w\nstderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	log.Printf("Render: wrote %s", outputKey)
	return nil
}
