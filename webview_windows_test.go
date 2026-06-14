// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

//go:build windows

package main

import (
	"testing"
	"unsafe"
)

// TestTaskDialogConfigSize guards against struct layout errors.
// TASKDIALOGCONFIG on 64-bit Windows is 176 bytes; a wrong size means
// TaskDialogIndirect reads garbage and the dialog silently fails or crashes.
func TestTaskDialogConfigSize(t *testing.T) {
	const want = 176
	if got := unsafe.Sizeof(taskDialogConfig{}); got != want {
		t.Errorf("taskDialogConfig size = %d bytes, want %d — struct layout does not match Windows SDK", got, want)
	}
}
