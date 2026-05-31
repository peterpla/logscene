package main

// render.go defines the Renderer interface and LocalRenderer, which invokes
// ffmpeg to assemble captured frames into a timelapse video.
//
// The renderer is co-located with the storage: LocalRenderer requires a
// LocalStorage. Future cloud renderers would pull frames, run ffmpeg (or a
// cloud transcoding service), and push the result back to cloud storage.

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
)

// Renderer assembles captured images into a timelapse video.
type Renderer interface {
	// Render collects all images whose storage key starts with prefix,
	// assembles them in lexicographic (chronological) order, and writes
	// the resulting video to outputKey.
	Render(ctx context.Context, prefix, outputKey string) error
}

// LocalRenderer uses ffmpeg to render frames stored on the local filesystem.
type LocalRenderer struct {
	store Storage // must be a *LocalStorage
}

// NewLocalRenderer creates a LocalRenderer backed by the given storage.
func NewLocalRenderer(store Storage) *LocalRenderer {
	return &LocalRenderer{store: store}
}

// Render lists all frames under prefix (e.g., a FolderPath), sorts them
// chronologically (filename order), runs ffmpeg, and writes the output video.
//
// ffmpeg is invoked with:
//
//	ffmpeg -framerate 24 -pattern_type glob -i '<dir>/<prefix>*.jpg' \
//	       -c:v libx264 -pix_fmt yuv420p <outputKey>
func (r *LocalRenderer) Render(ctx context.Context, prefix, outputKey string) error {
	frames, err := r.store.List(ctx, prefix)
	if err != nil {
		return fmt.Errorf("Render: list frames: %w", err)
	}
	if len(frames) == 0 {
		return fmt.Errorf("Render: no frames found for prefix %q", prefix)
	}
	log.Printf("Render: %d frames for %q → %s", len(frames), prefix, outputKey)

	// Derive the glob pattern from the directory and common prefix of the frames.
	dir := filepath.Dir(frames[0])
	base := filepath.Base(prefix)

	// Build ffmpeg command.
	// -y: overwrite output without prompting
	// -framerate 24: standard timelapse frame rate
	// -pattern_type glob: match files by glob
	// -i '<dir>/<base>*': input pattern
	// -c:v libx264 -pix_fmt yuv420p: H.264, widely compatible
	globPattern := filepath.Join(dir, base+"*")
	args := []string{
		"-y",
		"-framerate", "24",
		"-pattern_type", "glob",
		"-i", globPattern,
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
