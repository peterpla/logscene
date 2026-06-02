package main

import (
	"flag"
	"log"
	"os"
	"strconv"
)

// Config holds application-wide configuration.
type Config struct {
	Path     string // directory containing timelapse.json
	PollSecs int    // seconds between capture-due checks
	Port     string // HTTP listen port
	TzdbAPI  string // timezonedb.com API key (required)
	LogDir   string // directory for daily rotating log files
	Storage  string // storage backend: "local", "gcs", "s3"
	BaseDir  string // root storage location; webcam folder names are relative to this
}

// Load populates Config from the process flags and environment.
// Priority: flag > env var > default.
// Port checks PORT (Cloud Run standard) before TIMELAPSE_PORT.
func (c *Config) Load() {
	c.loadFrom(flag.CommandLine, os.Args[1:], os.Getenv)
}

// loadFrom is the testable core of Load. It accepts an explicit FlagSet,
// argument slice, and env-lookup function so tests can supply fakes.
func (c *Config) loadFrom(fs *flag.FlagSet, args []string, getenv func(string) string) {
	path    := fs.String("path",    "", "directory containing timelapse.json (env: TIMELAPSE_PATH, default: ./)")
	poll    := fs.Int("poll",       0,  "seconds between time checks (env: TIMELAPSE_POLL, default: 60)")
	port    := fs.String("port",    "", "HTTP port to listen on (env: PORT or TIMELAPSE_PORT, default: 8099)")
	tzdb    := fs.String("tzdb",    "", "timezonedb.com API key (env: TIMELAPSE_TZDB)")
	logdir  := fs.String("logdir",  "", "directory for daily log files (env: TIMELAPSE_LOGDIR, default: ./logs)")
	storage := fs.String("storage", "", "storage backend: local, gcs, s3 (env: TIMELAPSE_STORAGE, default: local)")
	base    := fs.String("base",    "", "root directory for captured images (env: TIMELAPSE_BASE, default: ./captures)")
	fs.Parse(args) //nolint:errcheck

	c.Path    = coalesce(*path,    getenv("TIMELAPSE_PATH"),    "./")
	c.Port    = coalesce(*port,    getenv("PORT"),              getenv("TIMELAPSE_PORT"), "8099")
	c.TzdbAPI = coalesce(*tzdb,    getenv("TIMELAPSE_TZDB"))
	c.LogDir  = coalesce(*logdir,  getenv("TIMELAPSE_LOGDIR"),  "./logs")
	c.Storage = coalesce(*storage, getenv("TIMELAPSE_STORAGE"), "local")
	c.BaseDir = coalesce(*base,    getenv("TIMELAPSE_BASE"),    "./captures")

	if *poll != 0 {
		c.PollSecs = *poll
	} else if v := getenv("TIMELAPSE_POLL"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			log.Fatalf("Config: invalid TIMELAPSE_POLL %q: must be a positive integer", v)
		}
		c.PollSecs = n
	} else {
		c.PollSecs = 60
	}

	log.Printf("Config: path=%s poll=%d port=%s logdir=%s storage=%s base=%s tzdb_configured=%t",
		c.Path, c.PollSecs, c.Port, c.LogDir, c.Storage, c.BaseDir, c.TzdbAPI != "")
}

// coalesce returns the first non-empty string from vals.
func coalesce(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
