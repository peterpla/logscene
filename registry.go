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

// readLastRenderDate returns the stored last render date (YYYY-MM-DD), or "" if unset.
func readLastRenderDate() (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, regKeyPath, registry.QUERY_VALUE)
	if err != nil {
		// Key absent is normal before any registry write; any other failure is unexpected.
		return "", fmt.Errorf("registry: open %s: %w", regKeyPath, err)
	}
	defer k.Close()

	val, _, err := k.GetStringValue("LastRenderDate")
	if err != nil {
		return "", nil // value not yet written — normal on first grace-period render
	}
	return val, nil
}

// writeLastRenderDate persists a render date (YYYY-MM-DD) for grace-period enforcement.
func writeLastRenderDate(date string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, regKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("registry: open %s for write: %w", regKeyPath, err)
	}
	defer k.Close()

	if err := k.SetStringValue("LastRenderDate", date); err != nil {
		return fmt.Errorf("registry: write LastRenderDate: %w", err)
	}
	return nil
}
