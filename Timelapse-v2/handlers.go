package main

// handlers.go contains all HTTP request handlers.

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
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
		wc.FolderPath = strings.TrimSpace(r.FormValue("folder"))
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
		if wc.Additional < 0 || wc.Additional > 16 {
			http.Error(w, "additional must be 0–16", http.StatusBadRequest)
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

		// For local storage, ensure the destination directory exists.
		if _, ok := s.storage.(*LocalStorage); ok {
			if err := os.MkdirAll(wc.FolderPath, 0755); err != nil {
				log.Printf("handleNew: MkdirAll: %v", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}

		s.mu.Lock()
		s.webcams.Append(wc)
		s.mu.Unlock()

		if err := s.webcams.Write(s.config.Path, s.validate); err != nil {
			log.Printf("handleNew: Write: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		s.wg.Add(1)
		go capture(s.ctx, wc, time.Duration(s.config.PollSecs)*time.Second, s)

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
			Webcam      string `json:"webcam"`
			NextCapture string `json:"next_capture"`
			In          string `json:"in"`
		}{
			Webcam:      nextName,
			NextCapture: nextTime.Format(time.RFC3339),
			In:          time.Until(nextTime).Truncate(time.Second).String(),
		})
	}
}

// handleLogs returns the last n lines of today's log file as plain text.
// The number of lines can be controlled with the ?n= query parameter (default 100).
// Useful when the server is running remotely and stdout is not accessible.
func (s *server) handleLogs() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		n := 100
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
	Folder string `json:"folder"`
	Output string `json:"output"`
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

		go func() {
			if err := s.renderer.Render(r.Context(), req.Folder, req.Output); err != nil {
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

// initTemplates parses all .html files in dir into s.tmpl.
func (s *server) initTemplates(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("initTemplates: ReadDir %s: %w", dir, err)
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".html") {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	if len(paths) == 0 {
		return fmt.Errorf("initTemplates: no .html files found in %s", dir)
	}
	tmpl, err := template.ParseFiles(paths...)
	if err != nil {
		return fmt.Errorf("initTemplates: %w", err)
	}
	s.tmpl = tmpl
	return nil
}
