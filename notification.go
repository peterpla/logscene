// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ButtonPair identifies the action buttons shown in a notification center entry.
type ButtonPair int

const (
	ButtonDiagnosticOptional ButtonPair = iota // [Send Diagnostic] [No Thanks]
	ButtonDiagnosticRequired                   // [Send Diagnostic] [Continue]
	ButtonDismissOnly                          // [Dismiss]
)

// Notification is one entry in the notification center.
type Notification struct {
	ID        string     `json:"id"`
	Timestamp time.Time  `json:"timestamp"`
	Title     string     `json:"title"`
	Message   string     `json:"message"`
	Buttons   ButtonPair `json:"buttons"`
	Dismissed bool       `json:"dismissed"`
}

// NotificationCenter holds all notifications and persists them as JSON under
// configPath/notifications.json. All methods are safe for concurrent use.
type NotificationCenter struct {
	mu      sync.RWMutex
	entries []Notification
	path    string
}

// newNotificationCenter creates a NotificationCenter backed by
// configPath/notifications.json. Existing entries are loaded from disk; a
// missing file is treated as an empty list.
func newNotificationCenter(configPath string) *NotificationCenter {
	nc := &NotificationCenter{
		path: filepath.Join(configPath, "notifications.json"),
	}
	if data, err := os.ReadFile(nc.path); err == nil {
		_ = json.Unmarshal(data, &nc.entries)
	}
	return nc
}

// Add appends a notification and persists the list. Timestamp is set to now;
// ID is set to a nanosecond timestamp string if not provided.
func (nc *NotificationCenter) Add(n Notification) {
	nc.mu.Lock()
	n.Timestamp = time.Now()
	if n.ID == "" {
		n.ID = fmt.Sprintf("%d", n.Timestamp.UnixNano())
	}
	nc.entries = append(nc.entries, n)
	nc.save()
	nc.mu.Unlock()
}

// Dismiss marks the entry with the given ID as dismissed and persists.
// Returns true if the entry was found.
func (nc *NotificationCenter) Dismiss(id string) bool {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	for i := range nc.entries {
		if nc.entries[i].ID == id {
			nc.entries[i].Dismissed = true
			nc.save()
			return true
		}
	}
	return false
}

// UnreadCount returns the number of non-dismissed entries.
func (nc *NotificationCenter) UnreadCount() int {
	nc.mu.RLock()
	defer nc.mu.RUnlock()
	n := 0
	for _, e := range nc.entries {
		if !e.Dismissed {
			n++
		}
	}
	return n
}

// HasUndismissed returns true if an entry with the given ID exists and has not been dismissed.
func (nc *NotificationCenter) HasUndismissed(id string) bool {
	nc.mu.RLock()
	defer nc.mu.RUnlock()
	for _, e := range nc.entries {
		if e.ID == id && !e.Dismissed {
			return true
		}
	}
	return false
}

// All returns a snapshot of all entries, newest first.
func (nc *NotificationCenter) All() []Notification {
	nc.mu.RLock()
	defer nc.mu.RUnlock()
	out := make([]Notification, len(nc.entries))
	for i, e := range nc.entries {
		out[len(nc.entries)-1-i] = e
	}
	return out
}

// save persists entries to disk. Caller must hold nc.mu (write).
func (nc *NotificationCenter) save() {
	data, err := json.MarshalIndent(nc.entries, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(nc.path, data, 0644)
}
