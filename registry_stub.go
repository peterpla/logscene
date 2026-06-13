// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

//go:build !windows

package main

import "time"

// readOrSetInstallDate returns the current time so non-Windows builds
// always run in active-trial state.
func readOrSetInstallDate() (time.Time, error) { return time.Now(), nil }

