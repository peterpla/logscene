// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// webcam.go defines Webcam (one webcam's capture configuration) and Webcams
// (the full list, backed by timelapse.json).
//
// Naming note: "Webcam" describes what this struct actually is — the settings
// for one physical camera. When video rendering is added, the render
// configuration will live in a separate TimelapseSpec struct.

import (
	"encoding/json"
	"fmt"
	"log"
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
// Persisted fields are serialized to timelapse.json; runtime fields are
// tagged json:"-" and populated each day by SetCaptureTimes.
type Webcam struct {
	// --- persisted fields ---
	Name           string  `json:"name"           validate:"required"`
	URL            string  `json:"webcamUrl"      validate:"required,url"`
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
	Additional int    `json:"additional"`                    // 0–47 extra captures between first and last
	Folder     string `json:"folder"    validate:"required"` // short name relative to BaseDir, e.g. "kohm-yah-mah-nee"
	WebcamTZ   string `json:"webcamTZ,omitempty"`            // IANA timezone name, cached after first lookup
	Disabled   bool   `json:"disabled,omitempty"`            // true = skip at startup; operator-set

	// --- runtime fields (not persisted) ---
	mu           sync.RWMutex   `json:"-"`
	FirstFlags   uint           `json:"-"` // bitmask derived from First* booleans
	LastFlags    uint           `json:"-"` // bitmask derived from Last* booleans
	WebcamLoc    *time.Location `json:"-"` // loaded from WebcamTZ at runtime
	SunriseUTC   time.Time      `json:"-"`
	SolarNoonUTC time.Time      `json:"-"`
	SunsetUTC    time.Time      `json:"-"`
	CaptureTimes []time.Time    `json:"-"` // today's schedule, UTC
	NextCapture  int            `json:"-"` // index of next future time in CaptureTimes
	Backoff      time.Duration  `json:"-"` // exponential backoff for tier-1 outages
	FirstFailure time.Time      `json:"-"` // when current failure streak started; zero = no streak
	LastAttempt  time.Time      `json:"-"` // when capture was last attempted
}

// newWebcam returns an initialized Webcam with an empty capture-times slice.
func newWebcam() *Webcam {
	return &Webcam{CaptureTimes: []time.Time{}}
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

const masterFile = "timelapse.json"

// Webcams is the ordered list of all configured webcams, persisted to timelapse.json.
type Webcams []*Webcam

func newWebcams() *Webcams {
	ws := new(Webcams)
	return ws
}

// Read loads and validates timelapse.json into ws.
func (ws *Webcams) Read(path string, validate *validator.Validate) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("Webcams.Read: %w", err)
	}
	if len(data) == 0 {
		return fmt.Errorf("Webcams.Read: file is empty")
	}

	if err := json.Unmarshal(data, ws); err != nil {
		return fmt.Errorf("Webcams.Read: unmarshal: %w", err)
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

// Write validates and writes ws to timelapse.json.
func (ws Webcams) Write(dir string, validate *validator.Validate) error {
	for i, wc := range ws {
		if err := validate.Struct(wc); err != nil {
			return fmt.Errorf("Webcams.Write: element %d (%s): %w", i, wc.Name, err)
		}
	}

	buf, err := json.MarshalIndent(ws, "", "  ")
	if err != nil {
		return fmt.Errorf("Webcams.Write: marshal: %w", err)
	}

	path := filepath.Join(dir, masterFile)
	if err := os.WriteFile(path, buf, 0644); err != nil {
		return fmt.Errorf("Webcams.Write: %w", err)
	}
	log.Printf("Webcams.Write: wrote %s", path)
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
