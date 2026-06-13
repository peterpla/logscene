// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// handlers.go contains all HTTP request handlers.

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"
)

// webcamCardData summarises one webcam for display on the dashboard.
type webcamCardData struct {
	Name            string
	Folder          string
	SourceType      string
	IntervalMinutes int
	StatusLabel     string
	StatusClass     string // Bootstrap bg-* colour token
	NextCapture     string // formatted time or short status string
	CapturesToday   int
	CanRender       bool // true if capture folder contains 2+ .jpg files on disk
}

// dashboardData is the template context for the dashboard page.
type dashboardData struct {
	Page       string
	Title      string
	Trial      TrialState
	Webcams    []webcamCardData
	RendersDir string // OS-native path to BaseDir/renders for display in render modal
}

// newWebcamData is the template context for the add-webcam form.
type newWebcamData struct {
	Page  string
	Title string
	Trial TrialState
}

// writeFailureData is the template context for the Write failure modal.
type writeFailureData struct {
	Page          string
	Title         string
	Trial         TrialState
	Webcam        *Webcam
	SourceLabel   string // "URL" or "Device"
	SourceValue   string // wc.URL or wc.DeviceName
	ScheduleDesc  string // "sunrise to sunset−30 min"
	ClipboardText string
}

// writeFailureFields computes the display and clipboard fields for writeFailureData.
func writeFailureFields(wc *Webcam) (sourceLabel, sourceValue, scheduleDesc, clipboardText string) {
	if wc.SourceType == "usb" || wc.SourceType == "stream" {
		sourceLabel, sourceValue = "Device", wc.DeviceName
	} else {
		sourceLabel, sourceValue = "URL", wc.URL
	}

	var first, last string
	switch {
	case wc.FirstSunrise:
		first = "sunrise"
	case wc.FirstSunrise30:
		first = "sunrise+30 min"
	case wc.FirstSunrise60:
		first = "sunrise+60 min"
	case wc.FirstTime:
		first = wc.FirstTimeValue
	}
	switch {
	case wc.LastSunset:
		last = "sunset"
	case wc.LastSunset30:
		last = "sunset−30 min"
	case wc.LastSunset60:
		last = "sunset−60 min"
	case wc.LastTime:
		last = wc.LastTimeValue
	}
	scheduleDesc = first + " to " + last

	clipboardText = fmt.Sprintf(
		"Name: %s\n%s: %s\nLatitude: %g\nLongitude: %g\nFolder: %s\nInterval: %d minutes\nSchedule: %s",
		wc.Name, sourceLabel, sourceValue, wc.Latitude, wc.Longitude, wc.Folder, wc.IntervalMinutes, scheduleDesc,
	)
	return
}

// webcamCard builds display data from a live Webcam, holding its read lock.
func webcamCard(wc *Webcam, baseDir string) webcamCardData {
	wc.mu.RLock()
	defer wc.mu.RUnlock()

	d := webcamCardData{
		Name:            wc.Name,
		Folder:          wc.Folder,
		SourceType:      wc.SourceType,
		IntervalMinutes: wc.IntervalMinutes,
	}
	if d.SourceType == "" {
		d.SourceType = "url"
	}

	switch {
	case wc.Disabled:
		d.StatusLabel, d.StatusClass = "Disabled", "secondary"
	case !wc.FirstFailure.IsZero():
		d.StatusLabel, d.StatusClass = "Error", "warning"
	default:
		d.StatusLabel, d.StatusClass = "Active", "success"
	}

	switch {
	case wc.DayFirst.IsZero():
		d.CapturesToday = 0
		d.NextCapture = "Pending"
	case wc.NextCaptureAt.IsZero():
		// Done for today — compute total scheduled captures.
		interval := time.Duration(wc.IntervalMinutes) * time.Minute
		if interval > 0 {
			d.CapturesToday = int(wc.DayLast.Sub(wc.DayFirst)/interval) + 1
		}
		d.NextCapture = "Done for today"
	default:
		interval := time.Duration(wc.IntervalMinutes) * time.Minute
		if interval > 0 {
			d.CapturesToday = int(wc.NextCaptureAt.Sub(wc.DayFirst) / interval)
		}
		if wc.WebcamLoc != nil {
			d.NextCapture = wc.NextCaptureAt.In(wc.WebcamLoc).Format("3:04 PM MST")
		} else {
			d.NextCapture = wc.NextCaptureAt.UTC().Format("15:04 UTC")
		}
	}

	// Assumes captures are flat files in BaseDir/Folder/. If subdirectory layout changes, update this glob.
	// For LocalStorage, handleNew guarantees this directory exists before the webcam is persisted.
	matches, _ := filepath.Glob(filepath.Join(baseDir, d.Folder, "*.jpg"))
	d.CanRender = len(matches) > 1

	return d
}

// handleHome renders the live dashboard.
func (s *server) handleHome() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		s.mu.RLock()
		cards := make([]webcamCardData, 0, len(*s.webcams))
		for _, wc := range *s.webcams {
			cards = append(cards, webcamCard(wc, s.config.BaseDir))
		}
		trial := s.trial
		s.mu.RUnlock()

		data := dashboardData{
			Page:       "dashboard",
			Title:      "Dashboard",
			Trial:      trial,
			Webcams:    cards,
			RendersDir: filepath.Join(filepath.FromSlash(s.config.BaseDir), "renders"),
		}
		if err := s.tmplDashboard.ExecuteTemplate(w, "base", data); err != nil {
			slog.Debug("template execution error", "handler", "handleHome", "failure_class", fcInternalError, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// handleGetNew renders the add-webcam form.
func (s *server) handleGetNew() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		s.mu.RLock()
		trial := s.trial
		s.mu.RUnlock()

		data := newWebcamData{
			Page:  "new-webcam",
			Title: "Add Webcam",
			Trial: trial,
		}
		if err := s.tmplNewWebcam.ExecuteTemplate(w, "base", data); err != nil {
			slog.Debug("template execution error", "handler", "handleGetNew", "failure_class", fcInternalError, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// handleNew processes the new-webcam form submission.
func (s *server) handleNew() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "form parse error: "+err.Error(), http.StatusBadRequest)
			return
		}

		wc := newWebcam()
		wc.Name = strings.TrimSpace(r.FormValue("name"))
		wc.URL = strings.TrimSpace(r.FormValue("webcamUrl"))
		wc.Folder = strings.TrimSpace(r.FormValue("folder"))
		wc.SourceType = strings.TrimSpace(r.FormValue("sourceType"))
		wc.DeviceName = strings.TrimSpace(r.FormValue("deviceName"))
		wc.FirstTimeValue = r.FormValue("firstTimeValue")
		wc.LastTimeValue = r.FormValue("lastTimeValue")

		var err error
		if wc.Latitude, err = strconv.ParseFloat(r.FormValue("latitude"), 64); err != nil {
			http.Error(w, "invalid latitude", http.StatusBadRequest)
			return
		}
		if wc.Longitude, err = strconv.ParseFloat(r.FormValue("longitude"), 64); err != nil {
			http.Error(w, "invalid longitude", http.StatusBadRequest)
			return
		}
		if wc.IntervalMinutes, err = strconv.Atoi(r.FormValue("intervalMinutes")); err != nil {
			http.Error(w, "invalid interval", http.StatusBadRequest)
			return
		}

		// Support radio-button format (firstOption/lastOption) from the UI form,
		// and fall back to individual named fields for backward compatibility.
		switch r.FormValue("firstOption") {
		case "firstSunrise":
			wc.FirstSunrise = true
		case "firstSunrise30":
			wc.FirstSunrise30 = true
		case "firstSunrise60":
			wc.FirstSunrise60 = true
		case "firstTime":
			wc.FirstTime = true
		default:
			wc.FirstSunrise = r.FormValue("firstSunrise") != ""
			wc.FirstSunrise30 = r.FormValue("firstSunrise30") != ""
			wc.FirstSunrise60 = r.FormValue("firstSunrise60") != ""
			wc.FirstTime = r.FormValue("firstTime") != ""
		}
		switch r.FormValue("lastOption") {
		case "lastSunset":
			wc.LastSunset = true
		case "lastSunset30":
			wc.LastSunset30 = true
		case "lastSunset60":
			wc.LastSunset60 = true
		case "lastTime":
			wc.LastTime = true
		default:
			wc.LastSunset = r.FormValue("lastSunset") != ""
			wc.LastSunset30 = r.FormValue("lastSunset30") != ""
			wc.LastSunset60 = r.FormValue("lastSunset60") != ""
			wc.LastTime = r.FormValue("lastTime") != ""
		}

		if err := s.validate.Struct(wc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := wc.SetFirstLastFlags(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Reject folder values that escape BaseDir (path traversal).
		base := filepath.Clean(s.config.BaseDir)
		resolved := filepath.Clean(filepath.Join(base, wc.Folder))
		if !strings.HasPrefix(resolved, base+string(filepath.Separator)) {
			http.Error(w, "invalid folder path", http.StatusBadRequest)
			return
		}

		// For local storage, pre-create the destination directory.
		if _, ok := s.storage.(*LocalStorage); ok {
			dir := filepath.Join(s.config.BaseDir, wc.Folder)
			if err := os.MkdirAll(dir, 0755); err != nil {
				slog.Info("capture directory could not be created", "webcam", wc.Name, "path", dir)
				slog.Debug("handleNew: MkdirAll failed", "webcam", wc.Name, "path", dir, "failure_class", fcFilesystem, "error", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}

		s.mu.Lock()
		if s.trial.capturesStopped() {
			s.mu.Unlock()
			http.Error(w, "upgrade required — trial captures have stopped", http.StatusForbidden)
			return
		}
		if len(*s.webcams) >= 1 {
			s.mu.Unlock()
			http.Error(w, "trial limited to 1 webcam — upgrade to add more", http.StatusForbidden)
			return
		}
		s.webcams.Append(wc)
		trial := s.trial
		wcCtx := s.webcamCtx
		s.mu.Unlock()

		if err := s.webcams.Write(s.config.Path, s.validate); err != nil {
			s.mu.Lock()
			s.webcams = s.webcams.Delete(wc.Name)
			s.mu.Unlock()
			slog.Info("webcam configuration could not be saved",
				"webcam", wc.Name,
				"path", filepath.Join(s.config.Path, masterFile))
			slog.Debug("handleNew: Write failed",
				"webcam", wc.Name,
				"path", filepath.Join(s.config.Path, masterFile),
				"failure_class", fcFilesystem,
				"error", err)
			sl, sv, sd, ct := writeFailureFields(wc)
			data := writeFailureData{
				Page:          "new-webcam",
				Title:         "Webcam Not Saved",
				Trial:         trial,
				Webcam:        wc,
				SourceLabel:   sl,
				SourceValue:   sv,
				ScheduleDesc:  sd,
				ClipboardText: ct,
			}
			w.WriteHeader(http.StatusInternalServerError)
			if tmplErr := s.tmplWriteFailure.ExecuteTemplate(w, "base", data); tmplErr != nil {
				slog.Debug("template execution error", "handler", "handleNew write failure", "failure_class", fcInternalError, "error", tmplErr)
			}
			return
		}

		s.webcamWg.Add(1)
		go capture(wcCtx, wc, time.Duration(s.config.PollSecs)*time.Second, s)

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// handleStatus returns a JSON summary of server health and webcam count.
func (s *server) handleStatus() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		s.mu.RLock()
		count := len(*s.webcams)
		s.mu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Status  string `json:"status"`
			Webcams int    `json:"webcams"`
			Uptime  string `json:"uptime"`
		}{
			Status:  "ok",
			Webcams: count,
			Uptime:  time.Since(s.startTime).Truncate(time.Second).String(),
		})
	}
}

// handleNext returns a fleet-wide capture status summary.
//
// Response fields:
//
//	capturing      — webcams whose NextCaptureAt is past with no failure streak (capture in flight)
//	retrying       — webcams whose NextCaptureAt is past with an active failure streak (backoff pending)
//	next_scheduled — soonest future NextCaptureAt across cameras not currently capturing or retrying;
//	                 omitted when all cameras are done for the day or none are configured
func (s *server) handleNext() httprouter.Handle {
	type nextScheduled struct {
		Webcam string `json:"webcam"`
		At     string `json:"at"`
		In     string `json:"in"`
	}
	type response struct {
		Capturing []string       `json:"capturing"`
		Retrying  []string       `json:"retrying"`
		Next      *nextScheduled `json:"next_scheduled,omitempty"`
	}

	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		resp := response{
			Capturing: []string{},
			Retrying:  []string{},
		}

		now := time.Now()
		var nextTime time.Time
		var nextName string

		s.mu.RLock()
		for _, wc := range *s.webcams {
			wc.mu.RLock()
			nca := wc.NextCaptureAt
			inFailureStreak := !wc.FirstFailure.IsZero()
			name := wc.Name
			wc.mu.RUnlock()

			if nca.IsZero() {
				continue
			}
			if now.After(nca) {
				if inFailureStreak {
					resp.Retrying = append(resp.Retrying, name)
				} else {
					resp.Capturing = append(resp.Capturing, name)
				}
			} else if nextTime.IsZero() || nca.Before(nextTime) {
				nextTime = nca
				nextName = name
			}
		}
		s.mu.RUnlock()

		if !nextTime.IsZero() {
			resp.Next = &nextScheduled{
				Webcam: nextName,
				At:     nextTime.UTC().Format(time.RFC3339),
				In:     time.Until(nextTime).Truncate(time.Second).String(),
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// handleLogs renders the last n lines of today's log file inside the base layout.
// The number of lines can be controlled with the ?n= query parameter (default 20).
func (s *server) handleLogs() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		n := 20
		if qn := r.URL.Query().Get("n"); qn != "" {
			if parsed, err := strconv.Atoi(qn); err == nil && parsed > 0 {
				n = parsed
			}
		}

		var logLines string
		logPath := filepath.Join(s.config.LogDir, "logscene-"+time.Now().Format("2006-01-02")+".log")
		raw, err := os.ReadFile(logPath)
		if err == nil {
			lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
			if len(lines) > n {
				lines = lines[len(lines)-n:]
			}
			logLines = strings.Join(lines, "\n")
		}

		data := struct {
			Page     string
			Title    string
			Trial    TrialState
			LogLines string
			NotFound bool
		}{"logs", "Logs", s.trial, logLines, err != nil}
		if err := s.tmplLogs.ExecuteTemplate(w, "base", data); err != nil {
			slog.Debug("template execution error", "handler", "handleLogs", "failure_class", fcInternalError, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// renderJobStatus is stored in server.renderJobs and returned by GET /render/status.
type renderJobStatus struct {
	Status  string `json:"status"`  // "rendering" | "complete" | "error"
	Message string `json:"message"` // full output path on complete; error detail on error
}

// renderRequest is the JSON body for POST /render.
type renderRequest struct {
	Folder string `json:"folder"`  // webcam folder name, e.g. "kohm-yah-mah-nee"
	Output string `json:"output"`  // output video file path
	Start  string `json:"start"`   // optional: YYYY-MM-DD inclusive lower bound
	End    string `json:"end"`     // optional: YYYY-MM-DD inclusive upper bound
	FPS    int    `json:"fps"`     // optional: frames per second (0 = default 24)
	Stride int    `json:"stride"`  // optional: keep every Nth frame (0 or 1 = every frame)
}

// handleRender triggers an ffmpeg render for a stored folder of images.
func (s *server) handleRender() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		switch s.trial {
		case TrialReadOnly:
			http.Error(w, "trial period ended — upgrade to render", http.StatusForbidden)
			return
		case TrialGraceRender:
			today := time.Now().Format("2006-01-02")
			last, err := readLastRenderDate()
			if err == nil && last == today {
				http.Error(w, "grace period: one render per day — try again tomorrow", http.StatusForbidden)
				return
			}
			_ = writeLastRenderDate(today)
		}

		var req renderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Folder == "" || req.Output == "" {
			http.Error(w, "folder and output are required", http.StatusBadRequest)
			return
		}

		rendersDir := filepath.Join(filepath.FromSlash(s.config.BaseDir), "renders")
		if err := os.MkdirAll(rendersDir, 0755); err != nil {
			slog.Info("renders directory could not be created", "path", rendersDir)
			slog.Debug("handleRender: MkdirAll failed", "path", rendersDir, "failure_class", fcFilesystem, "error", err)
			http.Error(w, "could not create renders directory", http.StatusInternalServerError)
			return
		}

		dir := filepath.Join(filepath.FromSlash(s.config.BaseDir), req.Folder)
		fullOutput := filepath.Join(rendersDir, filepath.Base(req.Output))
		opts := RenderOptions{
			FPS:       req.FPS,
			StartDate: strings.ReplaceAll(req.Start, "-", ""),
			EndDate:   strings.ReplaceAll(req.End, "-", ""),
			Stride:    req.Stride,
		}

		s.renderJobs.Store(fullOutput, renderJobStatus{Status: "rendering"})
		ctx := s.ctx
		go func() {
			if err := s.renderer.Render(ctx, dir, fullOutput, opts); err != nil {
				slog.Info("render failed", "webcam", req.Folder, "output", fullOutput)
				slog.Debug("handleRender: render failed", "webcam", req.Folder, "output", fullOutput, "failure_class", fcRenderFailure, "error", err)
				s.renderJobs.Store(fullOutput, renderJobStatus{Status: "error", Message: err.Error()})
			} else {
				ts := time.Now().Format("20060102_1504")
				ext := filepath.Ext(fullOutput)
				finalOutput := strings.TrimSuffix(fullOutput, ext) + "_" + ts + ext
				if err := os.Rename(fullOutput, finalOutput); err != nil {
					slog.Debug("handleRender: rename with timestamp failed, keeping original filename", "error", err)
					finalOutput = fullOutput
				}
				slog.Info("timelapse render complete", "webcam", req.Folder, "output", finalOutput)
				s.renderJobs.Store(fullOutput, renderJobStatus{Status: "complete", Message: finalOutput})
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Status string `json:"status"`
			Output string `json:"output"`
		}{"rendering", fullOutput})
	}
}

// handleRenderStatus returns the current status of an async render job.
// GET /render/status?output=<fullOutputPath>
// Terminal entries (complete or error) are deleted after the first read.
func (s *server) handleRenderStatus() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		output := r.URL.Query().Get("output")
		if output == "" {
			http.Error(w, "output parameter required", http.StatusBadRequest)
			return
		}
		v, ok := s.renderJobs.Load(output)
		if !ok {
			http.Error(w, "no render job found for output", http.StatusNotFound)
			return
		}
		entry := v.(renderJobStatus)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entry)
		if entry.Status != "rendering" {
			s.renderJobs.Delete(output)
		}
	}
}

// handleReload stops all capture goroutines, re-reads logscene.json, and
// restarts them. It validates the new config before stopping anything, so a
// bad config file leaves the running goroutines untouched.
// The response is returned after the new goroutines are launched; the 2 s
// stagger between launches means the handler blocks for ~2*(n-1) seconds.
func (s *server) handleReload() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		// Validate new config before disrupting anything.
		fresh := newWebcams()
		path := filepath.Join(s.config.Path, masterFile)
		if err := fresh.Read(path, s.validate); err != nil {
			slog.Debug("handleReload: config read failed", "error", err)
			http.Error(w, "reload failed: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Stop all capture goroutines and wait for them to exit.
		s.webcamCancel()
		s.webcamWg.Wait()

		// Swap in new config and create a fresh child context.
		s.mu.Lock()
		s.webcams = fresh
		s.webcamCtx, s.webcamCancel = context.WithCancel(s.ctx)
		newCtx := s.webcamCtx
		s.mu.Unlock()

		// Relaunch with 2 s stagger to respect timezonedb.com rate limit.
		if s.trial.capturesStopped() {
			slog.Debug("handleReload: trial expired — captures disabled", "trial", s.trial.String())
		} else {
			for i, wc := range *fresh {
				if i > 0 {
					time.Sleep(2 * time.Second)
				}
				s.webcamWg.Add(1)
				go capture(newCtx, wc, time.Duration(s.config.PollSecs)*time.Second, s)
			}
		}

		n := len(*fresh)
		slog.Debug("handleReload: complete", "webcams", n)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Status  string `json:"status"`
			Webcams int    `json:"webcams"`
		}{"reloaded", n})
	}
}

// handleInfo returns build version and date as JSON.
func (s *server) handleInfo() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Version   string `json:"version"`
			BuildDate string `json:"build_date"`
			GoVersion string `json:"go_version"`
		}{
			Version:   Version,
			BuildDate: BuildDate,
			GoVersion: runtime.Version(),
		})
	}
}

// handleDevices returns a JSON array of DirectShow video device names.
func (s *server) handleDevices() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		devices := listDirectShowVideoDevices()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(devices)
	}
}

// listDirectShowVideoDevices runs ffmpeg to enumerate DirectShow video devices
// and returns their display names. Returns an empty slice if ffmpeg is
// unavailable or no devices are found.
func listDirectShowVideoDevices() []string {
	cmd := exec.Command("ffmpeg", "-list_devices", "true", "-f", "dshow", "-i", "dummy")
	out, _ := cmd.CombinedOutput() // ffmpeg exits non-zero for -i dummy; ignore
	return parseDirectShowVideoDevices(out)
}

// parseDirectShowVideoDevices extracts video device names from ffmpeg dshow output.
// Handles two formats:
//   - New (ffmpeg 5+): [in#0 @ ...] "Name" (video)
//   - Old (ffmpeg 4.x): section between "DirectShow video devices" and "DirectShow audio devices" headers
func parseDirectShowVideoDevices(out []byte) []string {
	var devices []string
	inVideoSection := false

	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "Alternative name") {
			continue
		}
		// Old format: track section boundaries.
		if strings.Contains(line, "DirectShow video devices") {
			inVideoSection = true
			continue
		}
		if strings.Contains(line, "DirectShow audio devices") {
			inVideoSection = false
		}

		// New format: lines annotated with (video); old format: lines in the video section.
		if !inVideoSection && !strings.Contains(line, `" (video)`) {
			continue
		}

		if i := strings.Index(line, `"`); i >= 0 {
			rest := line[i+1:]
			if j := strings.Index(rest, `"`); j >= 0 {
				if name := rest[:j]; name != "" {
					devices = append(devices, name)
				}
			}
		}
	}

	if devices == nil {
		devices = []string{}
	}
	return devices
}

// handleProbe fetches one image from a URL-source camera and returns its size.
// Only url source type is supported; usb and stream require server-side hardware
// access that is deferred to a later implementation phase.
func (s *server) handleProbe() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		var req struct {
			URL        string `json:"url"`
			SourceType string `json:"sourceType"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}

		type probeResp struct {
			Bytes int64  `json:"bytes,omitempty"`
			Error string `json:"error,omitempty"`
		}
		respond := func(v probeResp) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(v)
		}

		if req.SourceType != "url" {
			respond(probeResp{Error: "test shot is only supported for Remote Camera (URL) sources"})
			return
		}
		if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
			respond(probeResp{Error: "URL must start with http:// or https://"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
		if err != nil {
			respond(probeResp{Error: "invalid URL: " + err.Error()})
			return
		}
		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			respond(probeResp{Error: "could not reach camera — check the URL and try again"})
			return
		}
		defer resp.Body.Close()

		const maxBytes = 10 << 20 // 10 MB guard
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
		if err != nil {
			respond(probeResp{Error: "error reading camera response"})
			return
		}
		respond(probeResp{Bytes: int64(len(body))})
	}
}

// handleGetLatlong renders the Find Coordinates stub page.
func (s *server) handleGetLatlong() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		data := struct {
			Page  string
			Title string
			Trial TrialState
		}{"", "Find Coordinates", s.trial}
		if err := s.tmplLatlong.ExecuteTemplate(w, "base", data); err != nil {
			slog.Debug("template execution error", "handler", "handleGetLatlong", "failure_class", fcInternalError, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// initTemplates parses the embedded HTML templates into per-page template sets.
// Each page gets its own clone of base.html so their "content" blocks don't
// conflict with each other.
func (s *server) initTemplates() error {
	base, err := template.ParseFS(templateFS, "templates/base.html")
	if err != nil {
		return fmt.Errorf("initTemplates: parse base: %w", err)
	}
	parse := func(name string) (*template.Template, error) {
		t, err := template.Must(base.Clone()).ParseFS(templateFS, "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("initTemplates: parse %s: %w", name, err)
		}
		return t, nil
	}

	if s.tmplDashboard, err = parse("dashboard"); err != nil {
		return err
	}
	if s.tmplNewWebcam, err = parse("new_webcam"); err != nil {
		return err
	}
	if s.tmplLatlong, err = parse("latlong"); err != nil {
		return err
	}
	if s.tmplLogs, err = parse("logs"); err != nil {
		return err
	}
	if s.tmplWriteFailure, err = parse("write_failure"); err != nil {
		return err
	}
	return nil
}
