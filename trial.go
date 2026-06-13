// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

import "time"

// TrialState represents the current free-trial enforcement mode.
type TrialState int

const (
	TrialActive  TrialState = iota // days 0–29: normal operation, 1-webcam cap
	TrialWarning                   // day 30: same as active; UI shows upgrade banner
	TrialExpired                   // days 31+: captures stopped; renders unrestricted
)

func (ts TrialState) String() string {
	switch ts {
	case TrialActive:
		return "active"
	case TrialWarning:
		return "warning"
	case TrialExpired:
		return "expired"
	default:
		return "unknown"
	}
}

// capturesStopped returns true when the trial no longer permits new captures.
func (ts TrialState) capturesStopped() bool { return ts >= TrialExpired }

// Exported methods for use in HTML templates.
func (ts TrialState) IsWarning() bool       { return ts == TrialWarning }
func (ts TrialState) IsExpired() bool       { return ts == TrialExpired }
func (ts TrialState) CapturesStopped() bool { return ts >= TrialExpired }

// computeTrialState derives the current trial state from the install date.
func computeTrialState(installDate time.Time) TrialState {
	days := int(time.Since(installDate).Hours() / 24)
	switch {
	case days <= 29:
		return TrialActive
	case days == 30:
		return TrialWarning
	default:
		return TrialExpired
	}
}
