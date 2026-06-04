// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

import "embed"

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS
