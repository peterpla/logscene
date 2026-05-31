package main

import (
	"log"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Config holds application-wide configuration.
type Config struct {
	Path     string // directory containing timelapse.json
	PollSecs int    // seconds between capture-due checks
	Port     string // HTTP listen port
	TzdbAPI  string // timezonedb.com API key (required)
	LogDir   string // directory for daily rotating log files
	Storage  string // storage backend: "local", "gcs", "s3"
}

// Load populates Config from flags, environment variables, and defaults.
// Priority: flag > env var (TIMELAPSE_*) > default.
func (c *Config) Load() {
	pflag.StringVar(&c.Path, "path", "./", "path to folder containing timelapse.json")
	pflag.IntVar(&c.PollSecs, "poll", 60, "seconds between time checks")
	pflag.StringVar(&c.Port, "port", "8099", "HTTP port to listen on")
	pflag.StringVar(&c.TzdbAPI, "tzdb", "", "API key for timezonedb.com")
	pflag.StringVar(&c.LogDir, "logdir", "./logs", "directory for daily log files")
	pflag.StringVar(&c.Storage, "storage", "local", "storage backend: local, gcs, s3")

	var help bool
	pflag.BoolVarP(&help, "help", "h", false, "show usage information")
	pflag.Parse()

	if help {
		pflag.PrintDefaults()
		// os.Exit called by caller to allow testing
		return
	}

	viper.BindPFlag("path", pflag.Lookup("path"))
	viper.BindPFlag("poll", pflag.Lookup("poll"))
	viper.BindPFlag("port", pflag.Lookup("port"))
	viper.BindPFlag("tzdb", pflag.Lookup("tzdb"))
	viper.BindPFlag("logdir", pflag.Lookup("logdir"))
	viper.BindPFlag("storage", pflag.Lookup("storage"))

	viper.SetEnvPrefix("timelapse")
	viper.AutomaticEnv()
	viper.BindEnv("path")
	viper.BindEnv("poll")
	viper.BindEnv("port")
	viper.BindEnv("tzdb")
	viper.BindEnv("logdir")
	viper.BindEnv("storage")

	c.Path = viper.GetString("path")
	c.PollSecs = viper.GetInt("poll")
	c.Port = viper.GetString("port")
	c.TzdbAPI = viper.GetString("tzdb")
	c.LogDir = viper.GetString("logdir")
	c.Storage = viper.GetString("storage")

	log.Printf("Config: path=%s poll=%d port=%s logdir=%s storage=%s tzdb_configured=%t",
		c.Path, c.PollSecs, c.Port, c.LogDir, c.Storage, c.TzdbAPI != "")
}
