// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

import (
	"testing"
	"time"
)

func TestComputeTrialState(t *testing.T) {
	cases := []struct {
		daysAgo int
		want    TrialState
	}{
		{0, TrialActive},
		{1, TrialActive},
		{29, TrialActive},
		{30, TrialWarning},
		{31, TrialGraceRender},
		{37, TrialGraceRender},
		{38, TrialReadOnly},
		{100, TrialReadOnly},
	}
	for _, tc := range cases {
		installDate := time.Now().AddDate(0, 0, -tc.daysAgo)
		got := computeTrialState(installDate)
		if got != tc.want {
			t.Errorf("day %d: want %s, got %s", tc.daysAgo, tc.want, got)
		}
	}
}

func TestTrialStateString(t *testing.T) {
	cases := []struct {
		state TrialState
		want  string
	}{
		{TrialActive, "active"},
		{TrialWarning, "warning"},
		{TrialGraceRender, "grace-render"},
		{TrialReadOnly, "read-only"},
		{TrialState(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.state.String(); got != tc.want {
			t.Errorf("TrialState(%d).String() = %q, want %q", tc.state, got, tc.want)
		}
	}
}
