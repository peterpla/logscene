package main

// version.go declares build-time variables injected via -ldflags.
// Default values apply when built without ldflags (e.g., go run or go test).

var (
	Version   = "dev"
	BuildDate = "unknown"
)
