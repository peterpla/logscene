// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

//go:build windows

package main

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows/registry"
)

const regKeyPath = `Software\LogScene`

// readOrSetInstallDate reads InstallDate (YYYY-MM-DD) from the registry.
// On first call (value absent) it writes today's date and returns it.
func readOrSetInstallDate() (time.Time, error) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, regKeyPath,
		registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return time.Time{}, fmt.Errorf("registry: open %s: %w", regKeyPath, err)
	}
	defer k.Close()

	val, _, err := k.GetStringValue("InstallDate")
	if err != nil {
		today := time.Now().Format("2006-01-02")
		if werr := k.SetStringValue("InstallDate", today); werr != nil {
			return time.Time{}, fmt.Errorf("registry: write InstallDate: %w", werr)
		}
		val = today
	}

	t, err := time.Parse("2006-01-02", val)
	if err != nil {
		return time.Time{}, fmt.Errorf("registry: parse InstallDate %q: %w", val, err)
	}
	return t, nil
}

