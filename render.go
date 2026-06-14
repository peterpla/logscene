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
	"errors"
	"fmt"
	"log/slog"
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
	Stride    int    // keep every Nth frame (0 or 1 = every frame)
}

// Renderer assembles captured images into a timelapse video.
type Renderer interface {
	// Render collects all images in dir, optionally filtered to a date range,
	// assembles them in chronological order, and writes the resulting video
	// to outputKey.
	Render(ctx context.Context, dir, outputKey string, opts RenderOptions) error
}

// RenderError is returned by LocalRenderer.Render for all failure cases.
// Class is a failure_class constant for log analysis; Message is a
// user-facing string suitable for display in the render modal.
type RenderError struct {
	Class   string
	Message string
	Err     error
}

func (e *RenderError) Error() string { return e.Message }
func (e *RenderError) Unwrap() error { return e.Err }

// classifyFFmpegError maps a cmd.Run error and captured stderr to a RenderError
// with an appropriate failure class and user-facing message.
func classifyFFmpegError(err error, stderr string) *RenderError {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &RenderError{Class: fcRenderCanceled, Message: "Render was interrupted when LogScene closed — try again", Err: err}
	}
	if errors.Is(err, exec.ErrNotFound) {
		return &RenderError{Class: fcRenderFFmpegMissing, Message: "ffmpeg is not installed or not on the system PATH — install ffmpeg and restart LogScene", Err: err}
	}
	s := strings.ToLower(stderr)
	switch {
	case strings.Contains(s, "encoder libx264 not found") || strings.Contains(s, "unknown encoder 'libx264'"):
		return &RenderError{Class: fcRenderCodecMissing, Message: "The H.264 encoder (libx264) is not available in this ffmpeg build — install a full ffmpeg build from ffmpeg.org", Err: err}
	case strings.Contains(s, "no space left on device") || strings.Contains(s, "not enough space on the disk") || strings.Contains(s, "disk write error"):
		return &RenderError{Class: fcRenderDiskFull, Message: "Not enough disk space to write the video — free up space and try again", Err: err}
	case strings.Contains(s, "permission denied") || strings.Contains(s, "access is denied"):
		return &RenderError{Class: fcRenderPermission, Message: "LogScene cannot write to the renders folder — check folder permissions", Err: err}
	default:
		return &RenderError{Class: fcRenderFFmpegError, Message: "ffmpeg exited with an unexpected error — check the Logs page for details", Err: err}
	}
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
//	       -vf crop=trunc(iw/2)*2:trunc(ih/2)*2 \
//	       -c:v libx264 -pix_fmt yuv420p <outputKey>
//
// The crop filter is a no-op for standard even-dimension frames and silently
// trims 1px on any odd dimension to satisfy the libx264/yuv420p requirement.
func (r *LocalRenderer) Render(ctx context.Context, dir, outputKey string, opts RenderOptions) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return &RenderError{Class: fcRenderInternal, Message: "An unexpected error occurred during rendering — check the Logs page for details", Err: fmt.Errorf("Render: readdir %s: %w", dir, err)}
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
		return &RenderError{Class: fcRenderNoFrames, Message: "No captures found for the selected date range — try a wider range or confirm captures exist", Err: fmt.Errorf("Render: no frames found in %q", dir)}
	}

	if opts.Stride > 1 {
		filtered := make([]string, 0, (len(frames)+opts.Stride-1)/opts.Stride)
		for i := 0; i < len(frames); i += opts.Stride {
			filtered = append(filtered, frames[i])
		}
		frames = filtered
	}

	slog.Info("starting timelapse render", "frames", len(frames), "outputKey", outputKey)
	slog.Debug("render started", "dir", dir, "outputKey", outputKey, "frames", len(frames))

	tmp, err := os.CreateTemp("", "logscene-concat-*.txt")
	if err != nil {
		return &RenderError{Class: fcRenderInternal, Message: "An unexpected error occurred during rendering — check the Logs page for details", Err: fmt.Errorf("Render: create concat file: %w", err)}
	}
	defer os.Remove(tmp.Name())

	for _, f := range frames {
		fmt.Fprintf(tmp, "file '%s'\n", filepath.ToSlash(f))
	}
	if err := tmp.Close(); err != nil {
		return &RenderError{Class: fcRenderInternal, Message: "An unexpected error occurred during rendering — check the Logs page for details", Err: fmt.Errorf("Render: close concat file: %w", err)}
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
		"-vf", "crop=trunc(iw/2)*2:trunc(ih/2)*2",
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		outputKey,
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return classifyFFmpegError(err, stderr.String())
	}
	slog.Info("timelapse render complete", "outputKey", outputKey, "frames", len(frames))
	slog.Debug("render complete", "outputKey", outputKey, "frames", len(frames))
	return nil
}
