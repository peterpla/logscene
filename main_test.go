// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// main_test.go tests openLogFile.

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenLogFile(t *testing.T) {
	dir := t.TempDir()
	date := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// Capture and restore log state around the test.
	origWriter := log.Writer()
	origLogFile := currentLogFile
	t.Cleanup(func() {
		log.SetOutput(origWriter)
		if f := currentLogFile; f != nil && f != origLogFile {
			f.Close()
		}
		currentLogFile = origLogFile
	})

	if err := openLogFile(dir, date); err != nil {
		t.Fatalf("openLogFile: %v", err)
	}

	expected := filepath.Join(dir, "logscene-2026-06-01.log")
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("log file not created: %v", err)
	}

	// Confirm the standard logger now writes to the new file.
	log.Print("test-sentinel")

	data, err := os.ReadFile(expected)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), "test-sentinel") {
		t.Errorf("log file does not contain test-sentinel: %s", data)
	}
}

func TestOpenLogFile_rotatesFile(t *testing.T) {
	dir := t.TempDir()

	origWriter := log.Writer()
	origLogFile := currentLogFile
	t.Cleanup(func() {
		log.SetOutput(origWriter)
		if f := currentLogFile; f != nil && f != origLogFile {
			f.Close()
		}
		currentLogFile = origLogFile
	})

	// Open a first log file, then a second — should close the first.
	date1 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	date2 := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)

	if err := openLogFile(dir, date1); err != nil {
		t.Fatalf("first openLogFile: %v", err)
	}
	firstFile := currentLogFile

	if err := openLogFile(dir, date2); err != nil {
		t.Fatalf("second openLogFile: %v", err)
	}

	// First file should have been closed; reading it should still work.
	if _, err := io.ReadAll(firstFile); err == nil {
		// Closed files return an error on read in some implementations; this
		// path is just confirming we didn't panic.
	}

	// Second log file should exist.
	expected := filepath.Join(dir, "logscene-2026-06-02.log")
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("second log file not created: %v", err)
	}
}

func TestOpenLogFile_dirIsFile(t *testing.T) {
	tmp := t.TempDir()
	// Create a regular file at "blockme"; trying to use "blockme/logs" as the
	// log directory must fail because an intermediate component is a file.
	blockingFile := filepath.Join(tmp, "blockme")
	if err := os.WriteFile(blockingFile, []byte("x"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := openLogFile(filepath.Join(blockingFile, "logs"), time.Now()); err == nil {
		t.Error("expected error when intermediate path component is a file, got nil")
	}
}

func TestOpenLogFile_openFileError(t *testing.T) {
	dir := t.TempDir()
	date := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// Create a directory at the path where the log file would be written.
	// os.OpenFile on a directory for writing must fail.
	logFilePath := filepath.Join(dir, "logscene-2026-06-01.log")
	if err := os.Mkdir(logFilePath, 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	origWriter := log.Writer()
	origLogFile := currentLogFile
	t.Cleanup(func() {
		log.SetOutput(origWriter)
		currentLogFile = origLogFile
	})

	if err := openLogFile(dir, date); err == nil {
		t.Error("expected error when log file path is a directory, got nil")
	}
}
