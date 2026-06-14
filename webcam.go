// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// webcam.go defines Webcam (one webcam's capture configuration) and Webcams
// (the full list, backed by logscene.json).
//
// Naming note: "Webcam" describes what this struct actually is — the settings
// for one physical camera. When video rendering is added, the render
// configuration will live in a separate TimelapseSpec struct.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-playground/validator/v10"
)

// Bitmask constants for first-capture selection.
const (
	flagFirstSunrise   uint = 1 << iota // at sunrise
	flagFirstSunrise30                  // sunrise + 30 min
	flagFirstSunrise60                  // sunrise + 60 min
	flagFirstTime                       // fixed clock time (webcam local)
)

// Bitmask constants for last-capture selection.
const (
	flagLastSunset   uint = 1 << iota // at sunset
	flagLastSunset30                  // sunset - 30 min
	flagLastSunset60                  // sunset - 60 min
	flagLastTime                      // fixed clock time (webcam local)
)

// Webcam holds everything needed to capture images from one physical camera.
//
// Persisted fields are serialized to logscene.json; runtime fields are
// tagged json:"-" and populated each day by SetCaptureTimes.
type Webcam struct {
	// --- persisted fields ---
	Name           string  `json:"name"           validate:"required"`
	URL            string  `json:"webcamUrl"      validate:"omitempty,url"`
	Latitude       float64 `json:"latitude"       validate:"required,latitude"`
	Longitude      float64 `json:"longitude"      validate:"required,longitude"`
	FirstSunrise   bool    `json:"firstSunrise"`
	FirstSunrise30 bool    `json:"firstSunrise30"`
	FirstSunrise60 bool    `json:"firstSunrise60"`
	FirstTime      bool    `json:"firstTime"`
	FirstTimeValue string  `json:"firstTimeValue"` // "HH:MM" in webcam local time; required when FirstTime true
	LastSunset     bool    `json:"lastSunset"`
	LastSunset30   bool    `json:"lastSunset30"`
	LastSunset60   bool    `json:"lastSunset60"`
	LastTime       bool    `json:"lastTime"`
	LastTimeValue  string  `json:"lastTimeValue"`  // "HH:MM" in webcam local time; required when LastTime true
	IntervalMinutes int `json:"intervalMinutes" validate:"min=1"` // minutes between captures; determines schedule density
	Folder     string `json:"folder"    validate:"required"` // short name relative to BaseDir, e.g. "kohm-yah-mah-nee"
	SourceType string `json:"sourceType,omitempty"`          // "url" (default) | "usb" | "stream"
	DeviceName string `json:"deviceName,omitempty"`          // DirectShow device name; required when SourceType == "usb"
	WebcamTZ   string `json:"webcamTZ,omitempty"`            // IANA timezone name, cached after first lookup
	Disabled        bool `json:"disabled,omitempty"`        // true = skip at startup; operator-set
	RecoveryPending bool `json:"recoveryPending,omitempty"` // true after Keep Trying on auto-suspend modal
	OddDimensions   bool `json:"oddDimensions,omitempty"`   // camera outputs odd-pixel-dimension frames; crop filter applied
	ProbeWidth      int  `json:"probeWidth,omitempty"`      // frame width from probe; used for odd-dimension notification
	ProbeHeight     int  `json:"probeHeight,omitempty"`     // frame height from probe

	// --- runtime fields (not persisted) ---
	mu           sync.RWMutex   `json:"-"`
	FirstFlags   uint           `json:"-"` // bitmask derived from First* booleans
	LastFlags    uint           `json:"-"` // bitmask derived from Last* booleans
	WebcamLoc    *time.Location `json:"-"` // loaded from WebcamTZ at runtime
	SunriseUTC   time.Time      `json:"-"`
	SolarNoonUTC time.Time      `json:"-"`
	SunsetUTC    time.Time      `json:"-"`
	DayFirst     time.Time      `json:"-"` // first scheduled capture today (UTC)
	DayLast      time.Time      `json:"-"` // last scheduled capture today (UTC)
	NextCaptureAt time.Time     `json:"-"` // next pending capture (UTC); zero = done or not yet set
	Backoff      time.Duration  `json:"-"` // exponential backoff for tier-1 outages
	FirstFailure time.Time      `json:"-"` // when current failure streak started; zero = no streak
	LastAttempt  time.Time      `json:"-"` // when capture was last attempted
	CaptureCountToday   int    `json:"-"` // captures completed today; seeded at goroutine startup
	ScheduledCountToday int    `json:"-"` // captures scheduled today; computed from DayFirst/DayLast
}

// newWebcam returns an initialized Webcam.
func newWebcam() *Webcam {
	return &Webcam{}
}

// SetFirstLastFlags derives FirstFlags and LastFlags from the First*/Last* booleans.
// It returns an error if not exactly one flag is set in each group.
func (wc *Webcam) SetFirstLastFlags() error {
	wc.FirstFlags = 0
	if wc.FirstSunrise {
		wc.FirstFlags |= flagFirstSunrise
	}
	if wc.FirstSunrise30 {
		wc.FirstFlags |= flagFirstSunrise30
	}
	if wc.FirstSunrise60 {
		wc.FirstFlags |= flagFirstSunrise60
	}
	if wc.FirstTime {
		wc.FirstFlags |= flagFirstTime
	}
	if bits.OnesCount(wc.FirstFlags) != 1 {
		return fmt.Errorf("Webcam %q: exactly one first-capture option must be selected (got %d)",
			wc.Name, bits.OnesCount(wc.FirstFlags))
	}
	if wc.FirstTime && wc.FirstTimeValue == "" {
		return fmt.Errorf("Webcam %q: firstTimeValue is required when firstTime is selected", wc.Name)
	}

	wc.LastFlags = 0
	if wc.LastSunset {
		wc.LastFlags |= flagLastSunset
	}
	if wc.LastSunset30 {
		wc.LastFlags |= flagLastSunset30
	}
	if wc.LastSunset60 {
		wc.LastFlags |= flagLastSunset60
	}
	if wc.LastTime {
		wc.LastFlags |= flagLastTime
	}
	if bits.OnesCount(wc.LastFlags) != 1 {
		return fmt.Errorf("Webcam %q: exactly one last-capture option must be selected (got %d)",
			wc.Name, bits.OnesCount(wc.LastFlags))
	}
	if wc.LastTime && wc.LastTimeValue == "" {
		return fmt.Errorf("Webcam %q: lastTimeValue is required when lastTime is selected", wc.Name)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Webcams — persisted list of all webcam configurations
// ---------------------------------------------------------------------------

const masterFile = "logscene.json"

// currentSchemaVersion is the logscene.json schema version written by this build.
// Increment when making breaking changes to the persisted data model.
const currentSchemaVersion = 1

// logsceneFile is the on-disk JSON envelope for logscene.json.
type logsceneFile struct {
	SchemaVersion int     `json:"schemaVersion"`
	Webcams       Webcams `json:"webcams"`
}

// Webcams is the ordered list of all configured webcams, persisted to logscene.json.
type Webcams []*Webcam

func newWebcams() *Webcams {
	ws := new(Webcams)
	return ws
}

// Read loads and validates logscene.json into ws.
// It accepts both the current wrapper format {"schemaVersion":N,"webcams":[...]} and
// the legacy bare-array format [...]; the latter logs a warning and continues.
func (ws *Webcams) Read(path string, validate *validator.Validate) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("Webcams.Read: %w", err)
	}
	if len(data) == 0 {
		return fmt.Errorf("Webcams.Read: file is empty")
	}

	var lf logsceneFile
	if err := json.Unmarshal(data, &lf); err != nil {
		// Legacy format: bare JSON array. Try direct unmarshal.
		if err2 := json.Unmarshal(data, ws); err2 != nil {
			return fmt.Errorf("Webcams.Read: unmarshal: %w", err2)
		}
		slog.Info("logscene.json uses a legacy format — resave by editing any webcam to upgrade")
	} else {
		*ws = lf.Webcams
		if lf.SchemaVersion < currentSchemaVersion {
			slog.Info("logscene.json schema version is outdated",
				"found", lf.SchemaVersion, "current", currentSchemaVersion)
		}
	}

	for i, wc := range *ws {
		if err := validate.Struct(wc); err != nil {
			return fmt.Errorf("Webcams.Read: element %d (%s): %w", i, wc.Name, err)
		}
		if err := wc.SetFirstLastFlags(); err != nil {
			return fmt.Errorf("Webcams.Read: element %d: %w", i, err)
		}
	}
	return nil
}

// Write validates and writes ws to logscene.json using the current wrapper format.
func (ws Webcams) Write(dir string, validate *validator.Validate) error {
	for i, wc := range ws {
		if err := validate.Struct(wc); err != nil {
			return fmt.Errorf("Webcams.Write: element %d (%s): %w", i, wc.Name, err)
		}
	}

	webcams := ws
	if webcams == nil {
		webcams = Webcams{}
	}
	lf := logsceneFile{SchemaVersion: currentSchemaVersion, Webcams: webcams}
	buf, err := json.MarshalIndent(lf, "", "  ")
	if err != nil {
		return fmt.Errorf("Webcams.Write: marshal: %w", err)
	}

	path := filepath.Join(dir, masterFile)
	if err := os.WriteFile(path, buf, 0644); err != nil {
		return fmt.Errorf("Webcams.Write: %w", err)
	}
	slog.Debug("config written", "path", path)
	return nil
}

// Append adds a Webcam to the list.
func (ws *Webcams) Append(wc *Webcam) {
	*ws = append(*ws, wc)
}

// Delete removes all Webcams whose Name starts with prefix. Used in tests.
func (ws *Webcams) Delete(prefix string) *Webcams {
	var keep Webcams
	for _, wc := range *ws {
		if !strings.HasPrefix(wc.Name, prefix) {
			keep = append(keep, wc)
		}
	}
	return &keep
}

// FindByName returns the index and pointer of the first Webcam with the given name,
// or -1 and nil if not found.
func (ws *Webcams) FindByName(name string) (int, *Webcam) {
	for i, wc := range *ws {
		if wc.Name == name {
			return i, wc
		}
	}
	return -1, nil
}

// Replace swaps the Webcam at index idx with wc.
func (ws *Webcams) Replace(idx int, wc *Webcam) {
	(*ws)[idx] = wc
}

// ---------------------------------------------------------------------------
// Validator
// ---------------------------------------------------------------------------

// newValidator returns a configured validator that includes struct-level
// source-type validation for Webcam.
func newValidator() *validator.Validate {
	v := validator.New()
	v.RegisterStructValidation(validateWebcamSource, Webcam{})
	return v
}

// validateWebcamSource enforces source-type-specific field requirements:
//   - SourceType "" or "url": URL required
//   - SourceType "stream":    URL required
//   - SourceType "usb":       DeviceName required
func validateWebcamSource(sl validator.StructLevel) {
	wc := sl.Current().Interface().(Webcam)
	st := wc.SourceType
	if st == "" {
		st = "url"
	}
	switch st {
	case "url", "stream":
		if wc.URL == "" {
			sl.ReportError(wc.URL, "URL", "webcamUrl", "required_for_source_type", "")
		}
	case "usb":
		if wc.DeviceName == "" {
			sl.ReportError(wc.DeviceName, "DeviceName", "deviceName", "required_for_usb", "")
		}
	default:
		sl.ReportError(wc.SourceType, "SourceType", "sourceType", "oneof", "url usb stream")
	}
}
