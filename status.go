// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

import "sync"

// WebcamStatus represents the display state of a webcam's capture health.
type WebcamStatus int

const (
	StatusActive   WebcamStatus = iota // green — capturing normally
	StatusIssues                       // yellow — transient failures
	StatusError                        // red — fatal or configuration error
	StatusDisabled                     // grey — manually or auto-suspended
)

func (s WebcamStatus) Label() string {
	switch s {
	case StatusActive:
		return "Active"
	case StatusIssues:
		return "Issues"
	case StatusError:
		return "Error"
	case StatusDisabled:
		return "Disabled"
	default:
		return "Unknown"
	}
}

func (s WebcamStatus) BadgeClass() string {
	switch s {
	case StatusActive:
		return "success"
	case StatusIssues:
		return "warning"
	case StatusError:
		return "danger"
	case StatusDisabled:
		return "secondary"
	default:
		return "secondary"
	}
}

func (s WebcamStatus) TooltipText() string {
	switch s {
	case StatusActive:
		return "Operating normally"
	case StatusIssues:
		return "Captures failing — check Logs for details"
	case StatusError:
		return "Configuration error — check Logs for details"
	case StatusDisabled:
		return "Captures paused"
	default:
		return ""
	}
}

type statusEntry struct {
	status  WebcamStatus
	title   string
	message string
}

// StatusCenter holds the most recent status vote for each webcam by name.
// The goroutine for each webcam calls Set to record its current health; the
// dashboard handler reads Get to render the badge.
type StatusCenter struct {
	mu      sync.RWMutex
	entries map[string]statusEntry
}

func newStatusCenter() *StatusCenter {
	return &StatusCenter{entries: make(map[string]statusEntry)}
}

// Set records a status vote for the named webcam, replacing any prior vote.
func (sc *StatusCenter) Set(webcamName string, s WebcamStatus, title, message string) {
	sc.mu.Lock()
	sc.entries[webcamName] = statusEntry{status: s, title: title, message: message}
	sc.mu.Unlock()
}

// Get returns the current status for the named webcam and whether a vote exists.
func (sc *StatusCenter) Get(webcamName string) (WebcamStatus, bool) {
	sc.mu.RLock()
	e, ok := sc.entries[webcamName]
	sc.mu.RUnlock()
	return e.status, ok
}

// Rename transfers the status entry from oldName to newName. No-op if oldName has no entry.
func (sc *StatusCenter) Rename(oldName, newName string) {
	sc.mu.Lock()
	if e, ok := sc.entries[oldName]; ok {
		sc.entries[newName] = e
		delete(sc.entries, oldName)
	}
	sc.mu.Unlock()
}
