// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// webcam_test.go tests SetFirstLastFlags, NextCaptureTime, Webcams.Read, and Webcams.Write.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-playground/validator/v10"
)

// ---------------------------------------------------------------------------
// SetFirstLastFlags
// ---------------------------------------------------------------------------

func TestSetFirstLastFlags_valid(t *testing.T) {
	wc := newWebcam()
	wc.Name = "test"
	wc.FirstSunrise = true
	wc.LastSunset = true
	if err := wc.SetFirstLastFlags(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wc.FirstFlags != flagFirstSunrise {
		t.Errorf("FirstFlags: want %b, got %b", flagFirstSunrise, wc.FirstFlags)
	}
	if wc.LastFlags != flagLastSunset {
		t.Errorf("LastFlags: want %b, got %b", flagLastSunset, wc.LastFlags)
	}
}

func TestSetFirstLastFlags_allVariants(t *testing.T) {
	cases := []struct {
		first bool // booleans in order: Sunrise, Sunrise30, Sunrise60, Time
		fs30  bool
		fs60  bool
		ft    bool
		ftv   string
		last  bool // booleans: Sunset, Sunset30, Sunset60, Time
		ls30  bool
		ls60  bool
		lt    bool
		ltv   string
		wantFirstFlags uint
		wantLastFlags  uint
	}{
		{first: true, last: true, wantFirstFlags: flagFirstSunrise, wantLastFlags: flagLastSunset},
		{fs30: true, ls30: true, wantFirstFlags: flagFirstSunrise30, wantLastFlags: flagLastSunset30},
		{fs60: true, ls60: true, wantFirstFlags: flagFirstSunrise60, wantLastFlags: flagLastSunset60},
		{ft: true, ftv: "07:00", lt: true, ltv: "18:00", wantFirstFlags: flagFirstTime, wantLastFlags: flagLastTime},
	}
	for _, tc := range cases {
		wc := newWebcam()
		wc.Name = "test"
		wc.FirstSunrise = tc.first
		wc.FirstSunrise30 = tc.fs30
		wc.FirstSunrise60 = tc.fs60
		wc.FirstTime = tc.ft
		wc.FirstTimeValue = tc.ftv
		wc.LastSunset = tc.last
		wc.LastSunset30 = tc.ls30
		wc.LastSunset60 = tc.ls60
		wc.LastTime = tc.lt
		wc.LastTimeValue = tc.ltv
		if err := wc.SetFirstLastFlags(); err != nil {
			t.Errorf("variant firstFlags=%b lastFlags=%b: unexpected error: %v",
				tc.wantFirstFlags, tc.wantLastFlags, err)
			continue
		}
		if wc.FirstFlags != tc.wantFirstFlags {
			t.Errorf("FirstFlags: want %b, got %b", tc.wantFirstFlags, wc.FirstFlags)
		}
		if wc.LastFlags != tc.wantLastFlags {
			t.Errorf("LastFlags: want %b, got %b", tc.wantLastFlags, wc.LastFlags)
		}
	}
}

func TestSetFirstLastFlags_noFirstFlag(t *testing.T) {
	wc := newWebcam()
	wc.Name = "test"
	wc.LastSunset = true
	if err := wc.SetFirstLastFlags(); err == nil {
		t.Error("expected error for no first flag, got nil")
	}
}

func TestSetFirstLastFlags_twoFirstFlags(t *testing.T) {
	wc := newWebcam()
	wc.Name = "test"
	wc.FirstSunrise = true
	wc.FirstSunrise30 = true
	wc.LastSunset = true
	if err := wc.SetFirstLastFlags(); err == nil {
		t.Error("expected error for two first flags, got nil")
	}
}

func TestSetFirstLastFlags_noLastFlag(t *testing.T) {
	wc := newWebcam()
	wc.Name = "test"
	wc.FirstSunrise = true
	if err := wc.SetFirstLastFlags(); err == nil {
		t.Error("expected error for no last flag, got nil")
	}
}

func TestSetFirstLastFlags_firstTimeRequiresValue(t *testing.T) {
	wc := newWebcam()
	wc.Name = "test"
	wc.FirstTime = true
	wc.LastSunset = true
	if err := wc.SetFirstLastFlags(); err == nil {
		t.Error("expected error for firstTime without firstTimeValue, got nil")
	}
}

func TestSetFirstLastFlags_lastTimeRequiresValue(t *testing.T) {
	wc := newWebcam()
	wc.Name = "test"
	wc.FirstSunrise = true
	wc.LastTime = true
	if err := wc.SetFirstLastFlags(); err == nil {
		t.Error("expected error for lastTime without lastTimeValue, got nil")
	}
}

// ---------------------------------------------------------------------------
// NextCaptureTime
// ---------------------------------------------------------------------------

func TestNextCaptureTime(t *testing.T) {
	wc := newWebcam()
	want := time.Date(2026, 6, 1, 14, 0, 0, 0, time.UTC)
	wc.NextCaptureAt = want

	if got := wc.NextCaptureTime(); !got.Equal(want) {
		t.Errorf("NextCaptureTime: want %v, got %v", want, got)
	}
}

// ---------------------------------------------------------------------------
// Webcams.Write and Webcams.Read
// ---------------------------------------------------------------------------

func validWebcam() *Webcam {
	wc := newWebcam()
	wc.Name = "Beach Cam"
	wc.URL = "http://example.com/cam.jpg"
	wc.Latitude = 34.01
	wc.Longitude = -118.49
	wc.Folder = "beach"
	wc.IntervalMinutes = 15
	wc.FirstSunrise = true
	wc.LastSunset = true
	_ = wc.SetFirstLastFlags()
	return wc
}

func TestWebcams_WriteAndRead(t *testing.T) {
	dir := t.TempDir()
	validate := validator.New()

	ws := newWebcams()
	ws.Append(validWebcam())

	if err := ws.Write(dir, validate); err != nil {
		t.Fatalf("Write: %v", err)
	}

	path := filepath.Join(dir, masterFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("logscene.json not written: %v", err)
	}

	ws2 := newWebcams()
	if err := ws2.Read(path, validate); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(*ws2) != 1 {
		t.Fatalf("Read: want 1 webcam, got %d", len(*ws2))
	}
	got := (*ws2)[0]
	if got.Name != "Beach Cam" {
		t.Errorf("Name: want %q, got %q", "Beach Cam", got.Name)
	}
	if got.URL != "http://example.com/cam.jpg" {
		t.Errorf("URL: want %q, got %q", "http://example.com/cam.jpg", got.URL)
	}
	if got.Latitude != 34.01 {
		t.Errorf("Latitude: want %v, got %v", 34.01, got.Latitude)
	}
}

func TestWebcams_Delete(t *testing.T) {
	ws := newWebcams()
	ws.Append(&Webcam{Name: "alpha"})
	ws.Append(&Webcam{Name: "beta"})
	ws.Append(&Webcam{Name: "alpha-2"})

	result := ws.Delete("alpha")
	if len(*result) != 1 {
		t.Fatalf("want 1 webcam after deleting prefix %q, got %d", "alpha", len(*result))
	}
	if (*result)[0].Name != "beta" {
		t.Errorf("want %q remaining, got %q", "beta", (*result)[0].Name)
	}
}

func TestWebcams_Read_missingFile(t *testing.T) {
	ws := newWebcams()
	if err := ws.Read("/nonexistent/path/logscene.json", validator.New()); err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestWebcams_Read_empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, masterFile)
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	ws := newWebcams()
	if err := ws.Read(path, validator.New()); err == nil {
		t.Error("expected error for empty file, got nil")
	}
}

func TestWebcams_Read_invalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, masterFile)
	if err := os.WriteFile(path, []byte("not json"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	ws := newWebcams()
	if err := ws.Read(path, validator.New()); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestWebcams_Write_validationError(t *testing.T) {
	dir := t.TempDir()
	// newWebcam() has empty Name, URL, etc. — all required fields are unset,
	// so validate.Struct will return an error before anything is written.
	ws := newWebcams()
	ws.Append(newWebcam())
	if err := ws.Write(dir, validator.New()); err == nil {
		t.Error("expected validation error for webcam with missing required fields, got nil")
	}
	// Confirm no file was written.
	if _, serr := os.Stat(filepath.Join(dir, masterFile)); serr == nil {
		t.Error("logscene.json should not have been created when validation fails")
	}
}

func TestWebcams_Read_validationError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, masterFile)
	// Webcam missing required fields (Name, URL, etc.).
	data, _ := json.Marshal([]map[string]any{{"latitude": 34.0, "longitude": -118.0}})
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	ws := newWebcams()
	if err := ws.Read(path, validator.New()); err == nil {
		t.Error("expected validation error, got nil")
	}
}

func TestWebcams_Read_setFirstLastFlagsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, masterFile)

	// validWebcam has FirstSunrise=true; adding FirstSunrise30=true gives
	// two first-capture flags → SetFirstLastFlags returns an error.
	wc := validWebcam()
	wc.FirstSunrise30 = true
	data, _ := json.Marshal(Webcams{wc})
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	ws := newWebcams()
	if err := ws.Read(path, newValidator()); err == nil {
		t.Error("expected error for webcam with conflicting first-capture flags, got nil")
	}
}

func TestWebcams_WriteAndRead_schemaVersion(t *testing.T) {
	dir := t.TempDir()
	ws := newWebcams()
	ws.Append(validWebcam())
	if err := ws.Write(dir, newValidator()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Verify the file contains the schemaVersion field.
	data, err := os.ReadFile(filepath.Join(dir, masterFile))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), `"schemaVersion"`) {
		t.Errorf("logscene.json should contain schemaVersion field; got: %s", data)
	}

	// Verify Read accepts the new format.
	ws2 := newWebcams()
	if err := ws2.Read(filepath.Join(dir, masterFile), newValidator()); err != nil {
		t.Fatalf("Read new format: %v", err)
	}
	if len(*ws2) != 1 {
		t.Errorf("want 1 webcam, got %d", len(*ws2))
	}
}

func TestWebcams_Read_outdatedSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, masterFile)

	// Write a file whose schemaVersion is below the current version.
	// Read should succeed (continue with warning) rather than error.
	lf := logsceneFile{SchemaVersion: 0, Webcams: Webcams{validWebcam()}}
	data, _ := json.Marshal(lf)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	ws := newWebcams()
	if err := ws.Read(path, newValidator()); err != nil {
		t.Fatalf("outdated schemaVersion should not cause an error: %v", err)
	}
	if len(*ws) != 1 {
		t.Errorf("want 1 webcam from outdated-schema file, got %d", len(*ws))
	}
}

func TestWebcams_Read_legacyBareArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, masterFile)

	// Write a bare JSON array (legacy format without schemaVersion wrapper).
	data, _ := json.Marshal(Webcams{validWebcam()})
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	ws := newWebcams()
	if err := ws.Read(path, newValidator()); err != nil {
		t.Fatalf("Read legacy format: %v", err)
	}
	if len(*ws) != 1 {
		t.Errorf("want 1 webcam from legacy format, got %d", len(*ws))
	}
}

func TestWebcams_Write_writeFileError(t *testing.T) {
	tmp := t.TempDir()
	// Pass a regular file as the directory so os.WriteFile(dir/logscene.json) fails.
	notADir := filepath.Join(tmp, "notadir")
	if err := os.WriteFile(notADir, []byte("x"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	ws := newWebcams()
	ws.Append(validWebcam())
	if err := ws.Write(notADir, newValidator()); err == nil {
		t.Error("expected error when dir is a regular file, got nil")
	}
}
