package main

// handlers.go contains all HTTP request handlers.

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"
)

// handleHome renders the new-webcam form.
func (s *server) handleHome() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		data := struct{ Company string }{"Timelapse"}
		if err := s.tmpl.ExecuteTemplate(w, "layout", data); err != nil {
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
		if wc.Additional, err = strconv.Atoi(r.FormValue("additional")); err != nil {
			http.Error(w, "invalid additional", http.StatusBadRequest)
			return
		}
		// 47 additional + 2 endpoints (first/last) = 49 shots across a ~12-hour day,
		// one every ~15 minutes — the finest interval that makes practical sense for
		// a landscape timelapse.
		if wc.Additional < 0 || wc.Additional > 47 {
			http.Error(w, "additional must be 0–47", http.StatusBadRequest)
			return
		}

		// Checkboxes are only present in the form when checked.
		wc.FirstSunrise = r.FormValue("firstSunrise") != ""
		wc.FirstSunrise30 = r.FormValue("firstSunrise30") != ""
		wc.FirstSunrise60 = r.FormValue("firstSunrise60") != ""
		wc.FirstTime = r.FormValue("firstTime") != ""
		wc.LastSunset = r.FormValue("lastSunset") != ""
		wc.LastSunset30 = r.FormValue("lastSunset30") != ""
		wc.LastSunset60 = r.FormValue("lastSunset60") != ""
		wc.LastTime = r.FormValue("lastTime") != ""

		if err := s.validate.Struct(wc); err != nil {
			log.Printf("handleNew: validate: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := wc.SetFirstLastFlags(); err != nil {
			log.Printf("handleNew: SetFirstLastFlags: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// For local storage, pre-create the destination directory.
		if _, ok := s.storage.(*LocalStorage); ok {
			dir := filepath.Join(s.config.BaseDir, wc.Folder)
			if err := os.MkdirAll(dir, 0755); err != nil {
				log.Printf("handleNew: MkdirAll: %v", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}

		s.mu.Lock()
		s.webcams.Append(wc)
		wcCtx := s.webcamCtx
		s.webcamWg.Add(1)
		s.mu.Unlock()

		if err := s.webcams.Write(s.config.Path, s.validate); err != nil {
			log.Printf("handleNew: Write: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

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

// handleNext returns JSON identifying the webcam with the soonest pending capture.
func (s *server) handleNext() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		s.mu.RLock()
		var nextName string
		var nextTime time.Time
		for _, wc := range *s.webcams {
			wc.mu.RLock()
			if wc.NextCapture < len(wc.CaptureTimes) {
				t := wc.CaptureTimes[wc.NextCapture]
				if nextTime.IsZero() || t.Before(nextTime) {
					nextTime = t
					nextName = wc.Name
				}
			}
			wc.mu.RUnlock()
		}
		s.mu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		if nextTime.IsZero() {
			json.NewEncoder(w).Encode(struct {
				Status string `json:"status"`
			}{"no captures scheduled"})
			return
		}
		json.NewEncoder(w).Encode(struct {
			Webcam           string `json:"webcam"`
			NextCapture      string `json:"next_capture"`
			NextCaptureLocal string `json:"next_capture_local"`
			In               string `json:"in"`
		}{
			Webcam:           nextName,
			NextCapture:      nextTime.Format(time.RFC3339),
			NextCaptureLocal: nextTime.In(time.Local).Format(time.RFC3339),
			In:               time.Until(nextTime).Truncate(time.Second).String(),
		})
	}
}

// handleLogs returns the last n lines of today's log file as plain text.
// The number of lines can be controlled with the ?n= query parameter (default 20).
// Useful when the server is running remotely and stdout is not accessible.
func (s *server) handleLogs() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		n := 20
		if qn := r.URL.Query().Get("n"); qn != "" {
			if parsed, err := strconv.Atoi(qn); err == nil && parsed > 0 {
				n = parsed
			}
		}

		logPath := filepath.Join(s.config.LogDir, "timelapse-"+time.Now().Format("2006-01-02")+".log")
		data, err := os.ReadFile(logPath)
		if err != nil {
			http.Error(w, "log file not available: "+err.Error(), http.StatusNotFound)
			return
		}

		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, strings.Join(lines, "\n"))
	}
}

// renderRequest is the JSON body for POST /render.
type renderRequest struct {
	Folder string `json:"folder"` // webcam folder name, e.g. "kohm-yah-mah-nee"
	Output string `json:"output"` // output video file path
	Start  string `json:"start"`  // optional: YYYY-MM-DD inclusive lower bound
	End    string `json:"end"`    // optional: YYYY-MM-DD inclusive upper bound
	FPS    int    `json:"fps"`    // optional: frames per second (0 = default 24)
}

// handleRender triggers an ffmpeg render for a stored folder of images.
func (s *server) handleRender() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		var req renderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Folder == "" || req.Output == "" {
			http.Error(w, "folder and output are required", http.StatusBadRequest)
			return
		}

		dir := filepath.Join(s.config.BaseDir, req.Folder)
		opts := RenderOptions{
			FPS:       req.FPS,
			StartDate: strings.ReplaceAll(req.Start, "-", ""),
			EndDate:   strings.ReplaceAll(req.End, "-", ""),
		}

		ctx := s.ctx
		go func() {
			if err := s.renderer.Render(ctx, dir, req.Output, opts); err != nil {
				log.Printf("handleRender: %v", err)
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Status string `json:"status"`
			Output string `json:"output"`
		}{"rendering", req.Output})
	}
}

// handleReload stops all capture goroutines, re-reads timelapse.json, and
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
			log.Printf("handleReload: read: %v", err)
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
		for i, wc := range *fresh {
			if i > 0 {
				time.Sleep(2 * time.Second)
			}
			s.webcamWg.Add(1)
			go capture(newCtx, wc, time.Duration(s.config.PollSecs)*time.Second, s)
		}

		n := len(*fresh)
		log.Printf("handleReload: complete, %d webcams running", n)
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

// initTemplates parses the embedded HTML templates into s.tmpl.
func (s *server) initTemplates() error {
	tmpl, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("initTemplates: %w", err)
	}
	s.tmpl = tmpl
	return nil
}
