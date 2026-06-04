// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

import (
	"flag"
	"testing"
)

func newFS() *flag.FlagSet {
	return flag.NewFlagSet("test", flag.ContinueOnError)
}

func noenv(string) string { return "" }

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// ---------------------------------------------------------------------------
// coalesce
// ---------------------------------------------------------------------------

func TestCoalesce(t *testing.T) {
	tests := []struct {
		name string
		vals []string
		want string
	}{
		{"all empty", []string{"", "", ""}, ""},
		{"first wins", []string{"a", "b", "c"}, "a"},
		{"skips leading empty", []string{"", "b", "c"}, "b"},
		{"last only", []string{"", "", "c"}, "c"},
		{"no args", []string{}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := coalesce(tc.vals...); got != tc.want {
				t.Errorf("coalesce(%v) = %q, want %q", tc.vals, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Config.loadFrom
// ---------------------------------------------------------------------------

func TestConfigDefaults(t *testing.T) {
	var c Config
	c.loadFrom(newFS(), nil, noenv)

	check := func(name, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	check("Path", c.Path, "./")
	check("Port", c.Port, "8099")
	check("LogDir", c.LogDir, "./logs")
	check("Storage", c.Storage, "local")
	check("BaseDir", c.BaseDir, "./captures")
	check("TzdbAPI", c.TzdbAPI, "")
	if c.PollSecs != 60 {
		t.Errorf("PollSecs = %d, want 60", c.PollSecs)
	}
}

func TestConfigEnvVars(t *testing.T) {
	var c Config
	c.loadFrom(newFS(), nil, envMap(map[string]string{
		"TIMELAPSE_PATH":    "/data",
		"TIMELAPSE_PORT":    "9000",
		"TIMELAPSE_POLL":    "30",
		"TIMELAPSE_TZDB":    "mykey",
		"TIMELAPSE_LOGDIR":  "/var/log",
		"TIMELAPSE_STORAGE": "gcs",
		"TIMELAPSE_BASE":    "/images",
	}))

	if c.Path != "/data" {
		t.Errorf("Path = %q, want /data", c.Path)
	}
	if c.Port != "9000" {
		t.Errorf("Port = %q, want 9000", c.Port)
	}
	if c.PollSecs != 30 {
		t.Errorf("PollSecs = %d, want 30", c.PollSecs)
	}
	if c.TzdbAPI != "mykey" {
		t.Errorf("TzdbAPI = %q, want mykey", c.TzdbAPI)
	}
	if c.LogDir != "/var/log" {
		t.Errorf("LogDir = %q, want /var/log", c.LogDir)
	}
	if c.Storage != "gcs" {
		t.Errorf("Storage = %q, want gcs", c.Storage)
	}
	if c.BaseDir != "/images" {
		t.Errorf("BaseDir = %q, want /images", c.BaseDir)
	}
}

func TestConfigPORTBeforeTIMELAPSE_PORT(t *testing.T) {
	var c Config
	c.loadFrom(newFS(), nil, envMap(map[string]string{
		"PORT":           "8080",
		"TIMELAPSE_PORT": "9000",
	}))
	if c.Port != "8080" {
		t.Errorf("Port = %q, want 8080 (PORT must take priority over TIMELAPSE_PORT)", c.Port)
	}
}

func TestConfigFlagOverridesEnv(t *testing.T) {
	var c Config
	c.loadFrom(newFS(), []string{"-port", "7777"}, envMap(map[string]string{
		"PORT":           "8080",
		"TIMELAPSE_PORT": "9000",
	}))
	if c.Port != "7777" {
		t.Errorf("Port = %q, want 7777 (flag must take priority over env vars)", c.Port)
	}
}

func TestConfigPollFlag(t *testing.T) {
	var c Config
	c.loadFrom(newFS(), []string{"-poll", "45"}, noenv)
	if c.PollSecs != 45 {
		t.Errorf("PollSecs = %d, want 45", c.PollSecs)
	}
}

func TestConfigPollFlagOverridesEnv(t *testing.T) {
	var c Config
	c.loadFrom(newFS(), []string{"-poll", "45"}, envMap(map[string]string{
		"TIMELAPSE_POLL": "120",
	}))
	if c.PollSecs != 45 {
		t.Errorf("PollSecs = %d, want 45 (flag must take priority over TIMELAPSE_POLL)", c.PollSecs)
	}
}
