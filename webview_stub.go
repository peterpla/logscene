// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

//go:build !windows

package main

import (
	"os"
	"os/signal"
)

// ensureSingleInstance is a no-op on non-Windows platforms.
func ensureSingleInstance() bool { return true }

// runUI blocks until SIGINT on non-Windows platforms where WebView2 is not
// available. The HTTP server is reachable at http://127.0.0.1:<port>.
func runUI(_ string, _ *NotificationCenter) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
	signal.Stop(c)
}
