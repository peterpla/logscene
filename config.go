// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

import (
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
)

// Config holds application-wide configuration.
type Config struct {
	Path     string // directory containing logscene.json
	PollSecs int    // seconds between capture-due checks
	// Port is intentionally absent — the server binds to an ephemeral loopback
	// port via net.Listen("tcp", "127.0.0.1:0") so the OS assigns it.
	TzdbAPI  string // timezonedb.com API key (required)
	LogDir   string // directory for daily rotating log files
	Storage  string // storage backend: "local", "gcs", "s3"
	BaseDir  string // root storage location; webcam folder names are relative to this
}

// Load populates Config from the process flags and environment.
// Priority: flag > env var > default.
// Port checks PORT (Cloud Run standard) before LOGSCENE_PORT.
func (c *Config) Load() {
	c.loadFrom(flag.CommandLine, os.Args[1:], os.Getenv)
}

// loadFrom is the testable core of Load. It accepts an explicit FlagSet,
// argument slice, and env-lookup function so tests can supply fakes.
func (c *Config) loadFrom(fs *flag.FlagSet, args []string, getenv func(string) string) {
	path    := fs.String("path",    "", "directory containing logscene.json (env: LOGSCENE_PATH, default: ./)")
	poll    := fs.Int("poll",       0,  "seconds between time checks (env: LOGSCENE_POLL, default: 60)")
	// -port / LOGSCENE_PORT intentionally removed — port is assigned by the OS at startup.
	tzdb    := fs.String("tzdb",    "", "timezonedb.com API key (env: LOGSCENE_TZDB)")
	logdir  := fs.String("logdir",  "", "directory for daily log files (env: LOGSCENE_LOGDIR, default: ./logs)")
	storage := fs.String("storage", "", "storage backend: local, gcs, s3 (env: LOGSCENE_STORAGE, default: local)")
	base    := fs.String("base",    "", "root directory for captured images (env: LOGSCENE_BASE, default: ./captures)")
	fs.Parse(args) //nolint:errcheck

	c.Path    = filepath.ToSlash(coalesce(*path,    getenv("LOGSCENE_PATH"),    "./"))
	c.TzdbAPI = coalesce(*tzdb,    getenv("LOGSCENE_TZDB"))
	c.LogDir  = filepath.ToSlash(coalesce(*logdir,  getenv("LOGSCENE_LOGDIR"),  "./logs"))
	c.Storage = coalesce(*storage, getenv("LOGSCENE_STORAGE"), "local")
	c.BaseDir = filepath.ToSlash(coalesce(*base,    getenv("LOGSCENE_BASE"),    "./captures"))

	if *poll != 0 {
		c.PollSecs = *poll
	} else if v := getenv("LOGSCENE_POLL"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			slog.Debug("invalid LOGSCENE_POLL value, using default", "value", v, "default", 60)
			c.PollSecs = 60
		} else {
			c.PollSecs = n
		}
	} else {
		c.PollSecs = 60
	}

	slog.Debug("config resolved",
		"path", c.Path,
		"pollSecs", c.PollSecs,
		"logDir", c.LogDir,
		"storage", c.Storage,
		"baseDir", c.BaseDir,
		"tzdbConfigured", c.TzdbAPI != "")
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
