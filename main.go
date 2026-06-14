// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// main.go defines the server struct, startup, shutdown, and logging.
// All external dependencies (HTTP clients, storage, renderer) are injected
// into the server at startup, so tests can substitute fakes.

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // embed IANA timezone database (required on Windows / scratch containers)

	"github.com/go-playground/validator/v10"
	"github.com/julienschmidt/httprouter"
)

// server holds all shared state and injected dependencies.
type server struct {
	router        *httprouter.Router
	validate      *validator.Validate
	config        *Config
	tmplDashboard     *template.Template
	tmplNewWebcam     *template.Template
	tmplLatlong       *template.Template
	tmplLogs          *template.Template
	tmplWriteFailure  *template.Template
	tmplNotifications *template.Template
	webcams      *Webcams       // all configured webcams; protected by mu
	storage      Storage
	renderer     Renderer
	tz           TimezoneClient
	solar        SolarClient
	fetcher      ImageFetcher
	ctx          context.Context
	cancel       context.CancelFunc
	webcamCtx    context.Context    // child of ctx; cancelled to stop capture goroutines
	webcamCancel context.CancelFunc // protected by mu when read in handleNew
	wg           sync.WaitGroup     // maintenance goroutines (newDayMaintenance)
	webcamWg     sync.WaitGroup     // capture goroutines only
	mu           sync.RWMutex       // protects webcams, webcamCtx, webcamCancel
	startTime    time.Time
	installDate  time.Time
	trial        TrialState
	renderJobs   sync.Map // fullOutputPath → renderJobStatus; entries deleted after first terminal read
	status        *StatusCenter
	notifications *NotificationCenter
}

var (
	currentLogFile      *os.File
	currentDebugLogFile *os.File
)

// multiHandler fans a single slog.Record out to multiple slog.Handler instances.
type multiHandler struct{ handlers []slog.Handler }

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, hh := range h.handlers {
		if hh.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, hh := range h.handlers {
		if hh.Enabled(ctx, r.Level) {
			_ = hh.Handle(ctx, r.Clone())
		}
	}
	return nil
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	hs := make([]slog.Handler, len(h.handlers))
	for i, hh := range h.handlers {
		hs[i] = hh.WithAttrs(attrs)
	}
	return &multiHandler{handlers: hs}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	hs := make([]slog.Handler, len(h.handlers))
	for i, hh := range h.handlers {
		hs[i] = hh.WithGroup(name)
	}
	return &multiHandler{handlers: hs}
}

func main() {
	if !ensureSingleInstance() {
		return
	}

	srv := newServer()

	path := filepath.Join(srv.config.Path, masterFile)
	if err := srv.webcams.Read(path, srv.validate); err != nil {
		log.Fatalf("main: read %s: %v", path, err)
	}

	if err := openLogFile(srv.config.LogDir, time.Now()); err != nil {
		slog.Info("LogScene cannot start: log files cannot be opened",
			"logDir", srv.config.LogDir, "error", err)
		// TODO Step 4.x: assembleSupportBundle + native MessageBox before exit
		os.Exit(1)
	}

	// Start daily log-rotation goroutine.
	srv.wg.Add(1)
	go newDayMaintenance(srv.ctx, srv)

	// Launch capture goroutines in the background so the UI window can open
	// immediately. The 2 s sleep between launches respects timezonedb.com's
	// 1 req/s rate limit. launchWg ensures all webcamWg.Add calls complete
	// before webcamWg.Wait is called during shutdown.
	var launchWg sync.WaitGroup
	launchWg.Add(1)
	go func() {
		defer launchWg.Done()
		if srv.trial.capturesStopped() {
			slog.Info("trial period ended — no new captures. Renders and existing images are unaffected.",
				"trialState", srv.trial.String())
			slog.Debug("trial state at goroutine launch — captures not started", "state", srv.trial.String())
			return
		}
		for _, wc := range *srv.webcams {
			srv.webcamWg.Add(1)
			go capture(srv.webcamCtx, wc, time.Duration(srv.config.PollSecs)*time.Second, srv)
			time.Sleep(2 * time.Second)
		}
	}()

	if err := srv.initTemplates(); err != nil {
		slog.Info("LogScene couldn't start — could not initialize templates. Try reinstalling LogScene.", "error", err)
		slog.Debug("initTemplates failed", "failure_class", fcInternalError, "error", err)
		// TODO Step 4.x: assembleSupportBundle + native MessageBox before exit
		os.Exit(1)
	}

	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		slog.Info("LogScene couldn't start — could not initialize static files. Try reinstalling LogScene.", "error", err)
		slog.Debug("fs.Sub staticFS failed", "failure_class", fcInternalError, "error", err)
		// TODO Step 4.x: assembleSupportBundle + native MessageBox before exit
		os.Exit(1)
	}
	srv.router.Handler("GET", "/static/*filepath",
		http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	srv.router.GET("/", srv.handleHome())
	srv.router.GET("/new", srv.handleGetNew())
	srv.router.GET("/latlong", srv.handleGetLatlong())
	srv.router.POST("/probe", srv.handleProbe())
	srv.router.GET("/devices", srv.handleDevices())
	srv.router.POST("/new", srv.handleNew())
	srv.router.GET("/info", srv.handleInfo())
	srv.router.GET("/status", srv.handleStatus())
	srv.router.GET("/next", srv.handleNext())
	srv.router.GET("/logs", srv.handleLogs())
	srv.router.POST("/render", srv.handleRender())
	srv.router.GET("/render/status", srv.handleRenderStatus())
	srv.router.POST("/reload", srv.handleReload())
	srv.router.GET("/notifications", srv.handleGetNotifications())
	srv.router.POST("/notifications/:id/dismiss", srv.handleDismissNotification())
	srv.router.GET("/open-file", srv.handleOpenFile())
	srv.router.GET("/open-folder", srv.handleOpenFolder())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		slog.Info("LogScene couldn't start — could not bind to a port.", "error", err)
		slog.Debug("net.Listen failed", "failure_class", fcInternalError, "error", err)
		os.Exit(1)
	}
	assignedPort := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	slog.Debug("port assigned", "port", assignedPort)

	hs := &http.Server{
		Handler:      srv.router,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	go startListening(hs, ln)
	go printStartupSummary(assignedPort)

	runUI(assignedPort, srv.notifications)
	slog.Debug("main: beginning shutdown")
	srv.cancel()
	launchWg.Wait() // ensure all webcamWg.Add calls complete before Wait
	srv.webcamWg.Wait()
	srv.wg.Wait()
	slog.Info("LogScene stopped")
	slog.Debug("main: shutdown complete")

	if currentLogFile != nil {
		currentLogFile.Close()
	}
}

// newServer constructs a server with production dependencies.
func newServer() *server {
	cfg := &Config{}
	cfg.Load()

	if cfg.TzdbAPI == "" {
		fmt.Fprintln(os.Stderr, "LOGSCENE_TZDB (or -tzdb flag) is required")
		os.Exit(1)
	}

	installDate, err := readOrSetInstallDate()
	if err != nil {
		slog.Info("LogScene couldn't start: unable to access settings storage.", "error", err)
		slog.Debug("trial data registry read failed", "failure_class", fcRegistry, "error", err)
		// TODO Step 4.x: assembleSupportBundle + native MessageBox before exit
		os.Exit(1)
	}
	trial := computeTrialState(installDate)
	slog.Debug("trial state determined",
		"state", trial.String(),
		"installDate", installDate.Format("2006-01-02"),
		"daysElapsed", int(time.Since(installDate).Hours()/24))

	store := buildStorage(cfg.Storage)

	ctx, cancel := context.WithCancel(context.Background())
	webcamCtx, webcamCancel := context.WithCancel(ctx)
	s := &server{
		router:       httprouter.New(),
		validate:     newValidator(),
		config:       cfg,
		webcams:      newWebcams(),
		storage:      store,
		renderer:     NewLocalRenderer(),
		tz:           NewHTTPTimezoneClient(cfg.TzdbAPI),
		solar:        NewHTTPSolarClient(),
		fetcher:      NewHTTPImageFetcher(),
		ctx:          ctx,
		cancel:       cancel,
		webcamCtx:    webcamCtx,
		webcamCancel: webcamCancel,
		startTime:     time.Now(),
		installDate:   installDate,
		trial:         trial,
		status:        newStatusCenter(),
		notifications: newNotificationCenter(cfg.Path),
	}
	return s
}

// buildStorage returns the Storage implementation for the configured backend.
func buildStorage(backend string) Storage {
	switch strings.ToLower(backend) {
	case "gcs":
		slog.Debug("storage backend not yet implemented, falling back to local", "requested", "gcs")
	case "s3":
		slog.Debug("storage backend not yet implemented, falling back to local", "requested", "s3")
	}
	return NewLocalStorage() // default: local
}

// startListening serves on the pre-bound listener and logs fatal errors.
func startListening(hs *http.Server, ln net.Listener) {
	if err := hs.Serve(ln); err != nil && err != http.ErrServerClosed {
		slog.Info("LogScene could not start the server.", "error", err)
		slog.Debug("server.Serve failed", "failure_class", fcInternalError, "error", err)
		// TODO Step 4.x: assembleSupportBundle + native MessageBox before exit
		os.Exit(1)
	}
}

// printStartupSummary is a placeholder; replaced by per-webcam slog.Debug snapshots
// and a "LogScene started successfully" slog.Info entry in the capture model refactor.
func printStartupSummary(_ string) {}

// newDayMaintenance wakes at server-local midnight each day to rotate the log file.
func newDayMaintenance(ctx context.Context, srv *server) {
	defer srv.wg.Done()
	for {
		now := time.Now()
		nextMidnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 1, 0, now.Location())
		select {
		case <-time.After(time.Until(nextMidnight)):
			if err := openLogFile(srv.config.LogDir, time.Now()); err != nil {
				slog.Info("LogScene encountered a problem managing its log files. Captures are continuing normally.")
				slog.Debug("newDayMaintenance: openLogFile failed", "failure_class", fcFilesystem, "error", err)
				// TODO Step 6i: add notification center entry
			}
		case <-ctx.Done():
			return
		}
	}
}

// openLogFile opens (or creates) a dated log file, redirects the standard
// logger to it, closes the previous log file, and reconfigures slog with a
// TextHandler (user-facing, Info+) and a JSONHandler (debug, Debug+).
func openLogFile(logDir string, date time.Time) error {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("openLogFile: MkdirAll %s: %w", logDir, err)
	}

	name := filepath.Join(logDir, "logscene-"+date.Format("2006-01-02")+".log")
	f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("openLogFile: %w", err)
	}
	log.SetOutput(f)
	if currentLogFile != nil {
		currentLogFile.Close()
	}
	currentLogFile = f

	// Open debug log (non-fatal: support bundle is best-effort).
	debugName := filepath.Join(logDir, "logscene-debug-"+date.Format("2006-01-02")+".log")
	df, debugErr := os.OpenFile(debugName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if debugErr != nil {
		slog.Info("LogScene encountered a problem with its diagnostic log. Captures are continuing normally.")
		slog.Debug("openLogFile: debug log could not be opened",
			"failure_class", fcFilesystem,
			"path", debugName,
			"error", debugErr)
		// TODO Step 6i: add notification center entry
	} else {
		if currentDebugLogFile != nil {
			currentDebugLogFile.Close()
		}
		currentDebugLogFile = df
	}

	// Wire slog: Info+ to user log; Debug+ to debug log (if open).
	userHandler := slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})
	var h slog.Handler = userHandler
	if currentDebugLogFile != nil {
		debugHandler := slog.NewJSONHandler(currentDebugLogFile, &slog.HandlerOptions{Level: slog.LevelDebug})
		h = &multiHandler{handlers: []slog.Handler{userHandler, debugHandler}}
	}
	slog.SetDefault(slog.New(h))

	slog.Debug("log file opened", "path", name)
	return nil
}

