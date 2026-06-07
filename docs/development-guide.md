# LogScene — Development Guide

*Last updated: 06-07-2026*

This guide documents everything needed to recreate the LogScene development environment
from scratch. The goal: a developer with no prior context can follow this document and
arrive at a fully working build, test, and deploy environment.

This document is stored in the logscene repo under /docs (i.e., *https://github.com/peterpla/logscene/docs*).

---

## Platform Notes

LogScene is a **Windows application**. The primary development platform is Windows.

Some parts of the development environment work on macOS or Linux (Go compilation,
WordPress plugin development, running tests that don't require hardware). But several
things are **Windows-only** and cannot be meaningfully developed or tested on another
platform:

| Capability | Windows | Mac/Linux |
|---|---|---|
| USB webcam capture (DirectShow) | ✓ Required | ✗ Not supported |
| IP camera capture (RTSP/MJPEG via ffmpeg) | ✓ | ✓ (ffmpeg works cross-platform) |
| `make build` / `make run` | ✓ | ✓ (the default result is not a Windows executable) |
| `make logs` | ✓ (PowerShell) | ✗ |
| `make deploy-wp` / `make deploy-staging` | ✓ (WinSCP required) | ✗ |
| Windows Registry (trial state, license key storage) | ✓ | ✗ |
| WebView2 wrapper (native app window) | ✓ | ✗ |
| Running the compiled app | ✓ | ✗ |

A developer working on a Mac can contribute to Go backend logic, WordPress plugin code,
and templates, but **must have access to a Windows machine for all testing and
deployment.** A Windows VM is acceptable for testing; native Windows is preferred for
day-to-day development.

---

## Prerequisites

### 1. Go

Install the Go toolchain from https://go.dev/dl/

- Minimum version: check `go.mod` for the current `go` directive
- Verify: `go version`
- The module path is `github.com/peterpla/logscene`

### 2. Git

- Install from https://git-scm.com/ (Windows) or via Homebrew (Mac)
- Verify: `git --version`
- Configure your name and email: `git config --global user.name "..."` and
  `git config --global user.email "..."`

### 3. ffmpeg

ffmpeg handles USB webcam capture (via DirectShow on Windows) and IP camera
capture (RTSP and MJPEG streams).

- Download a Windows build from https://ffmpeg.org/download.html (recommended:
  the gyan.dev or BtbN builds)
- Extract and place `ffmpeg.exe` somewhere on your PATH, or note its location
  for the `LOGSCENE_FFMPEG_PATH` environment variable
- Verify: `ffmpeg -version`

On Mac/Linux: `brew install ffmpeg` or your package manager. Note that DirectShow
(`-f dshow`) is Windows-only; USB webcam tests will not run on other platforms.

### 4. VS Code (recommended)

- Download from https://code.visualstudio.com/
- Recommended extensions:
  - **Go** (golang.go) — language support, debugging
  - **HTMX** — template hints
  - **PHP** — for WordPress plugin development
  - **GitLens** — git history and blame

VS Code launch configuration lives in `.vscode/launch.json` in the repo.

### 5. WinSCP (Windows only — required for deployment)

WinSCP is used by `make deploy-wp` and `make deploy-staging` to push the custom WordPress
plugin to the hosted and staging sites via SFTP.

- Download from https://winscp.net/
- Install to the default path: `C:\Program Files (x86)\WinSCP\`
- The Makefile references `winscp.com` (the scripting interface) at that path

If WinSCP is installed elsewhere, update the `WINSCP` variable in the Makefile.

### 6. Local by Flywheel (WordPress local development)

Local by Flywheel runs a local WordPress instance for developing and testing the
custom WordPress plugin without touching the live or staging site.

- Download from https://localwp.com/
- Create a site named `logscene` with the local domain `logscene.local`
- PHP version: 8.x (to match WordPress.com Business)
- After creating the site, manually copy the custom logscene custom plugin (`wp-plugin/logscene/`) to the Local
  site's `wp-content/plugins/` directory, or rebuild
- Verify libsodium is available: in the Local site's PHP shell, run
  `php -r "var_dump(extension_loaded('sodium'));"`  — should return `bool(true)`

### 7. Make

- Windows: install via [Chocolatey](https://chocolatey.org/) (`choco install make`)
  or [Scoop](https://scoop.sh/) (`scoop install make`), or use Git Bash which
  includes `make`
- Mac/Linux: available via Xcode command line tools or package manager

---

## Repository Setup

### Clone

```
git clone https://github.com/peterpla/logscene.git
cd logscene
```

The repo lives at `C:\Users\Peter\Coding\LogScene` on the primary dev machine.
**Do not** place the repo inside an OneDrive-synced folder — OneDrive and git
interact badly and can corrupt the repo index.

### Module dependencies

```
go mod tidy
```

This downloads all Go module dependencies declared in `go.mod`.

---

## Environment Variables

The following environment variables are used by the app and the Makefile.
Set them in your shell profile, in `.vscode/launch.json` for VS Code debugging,
or in a `.env` file (not committed to the repo).

| Variable | Purpose | Required |
|---|---|---|
| `LOGSCENE_PORT` | HTTP server port (default: 8080) | No |
| `LOGSCENE_LOGDIR` | Directory for log files | Yes (for `make logs`) |
| `LOGSCENE_FFMPEG_PATH` | Path to ffmpeg.exe if not on PATH | No |
| `LOGSCENE_LICENSE` | License key (headless deployments) | No |
| `WP_SFTP_PASSWORD` | SFTP password for production WordPress deploy | Yes (for deploy-wp) |
| `WP_STAGE_PASSWORD` | SFTP password for staging WordPress deploy | Yes (for deploy-staging) |

**Never commit credentials to the repo.** `WP_SFTP_PASSWORD` and `WP_STAGE_PASSWORD`
are sensitive — set them in your environment or a secrets manager only.

---

## Build and Run

All common tasks are in the Makefile.

| Command | What it does |
|---|---|
| `make build` | Compiles `logscene.exe` with version and build date embedded |
| `make run` | Builds then runs the app |
| `make test` | Runs the full test suite |
| `make test-local` | Runs tests with `TEST_STORAGE=local` (local filesystem storage) |
| `make logs` | Tails the most recent log file (Windows/PowerShell only) |
| `make tidy` | Runs `go mod tidy` |
| `make clean` | Removes the compiled binary |
| `make deploy-wp` | Deploys the WordPress plugin to production via WinSCP/SFTP |
| `make deploy-staging` | Deploys the WordPress plugin to the staging site via WinSCP/SFTP |

### Version embedding

`make build` embeds the current git tag and build timestamp into the binary via
`-ldflags`. On Windows, the build date uses PowerShell; on Mac/Linux it uses `date`.
The result is accessible at runtime via `main.Version` and `main.BuildDate`.

---

## WordPress Plugin Development

The custom WordPress plugin lives at `wp-plugin/logscene/logscene.php` in the repo.
It handles Paddle webhook events and license key distribution.

### Local development workflow

1. Make changes to files in `wp-plugin/logscene/`
2. Test against the Local by Flywheel instance at `http://logscene.local/`
3. Mock Paddle webhook POSTs locally — no tunnel required for unit/integration tests
4. When satisfied, deploy to staging: `make deploy-staging`
5. Smoke-test on the staging site
6. Deploy to production: `make deploy-wp`

### WordPress.com SFTP credentials

- **Production host:** `sftp.wp.com`
- **Production user:** `peter368925f397-hbcyk.wordpress.com`
- **Staging user:** `staging-0cf9-peter368925f397-hbcyk.wordpress.com`
- Password: stored in `WP_SFTP_PASSWORD` / `WP_STAGE_PASSWORD` environment variables

### Verifying a deployment

After `make deploy-wp`, log in to the WordPress.com dashboard and confirm the
logscene plugin appears as active under Plugins. Check the PHP error log via SFTP
or WP-CLI over SSH if anything appears broken.

---

## License Key Generation

License keys are generated **offline** on the dev machine. The Ed25519 private key
**never** touches WordPress or any networked system.

*(This section will be completed once the key generation tooling is built in Phase 2.
It will document the command-line tool, batch sizes, output format, and the upload
process for loading batches into the WordPress database.)*

### Private key storage

The Ed25519 private key is stored on a dedicated USB drive kept in a fire-resistant
safe. A backup copy of the drive is stored off-site. **Do not store the private key
on the dev machine's main drive, in cloud storage, or in the repo.**

---

## Testing

```
make test          # full suite
make test-local    # local storage variant
```

TO DO - what is the benefit of using `make test-local`?

### Hardware tests (Step 7)

USB webcam and IP camera tests require physical hardware and must be run on Windows.
See `phase1-checklist.md` Step 7 for the hardware list and test scope. These tests
are run manually against real devices before each public release.

---

## Repo Structure (overview)

```
logscene/
├── main.go               # Entry point; HTTP server setup
├── webcam.go             # Webcam struct, persistence
├── capture.go            # Capture goroutine, ffmpeg invocation
├── handlers.go           # HTTP handlers
├── schedule.go           # Schedule arithmetic
├── registry.go           # Windows Registry (trial state, license key)
├── config.go             # Configuration loading
├── render.go             # Timelapse render
├── static/               # Embedded static assets (Bootstrap, HTMX, CSS)
├── templates/            # Go HTML templates
├── wp-plugin/
│   └── logscene/
│       └── logscene.php  # Custom WordPress plugin (webhook handler, key distribution)
├── docs/
│   ├── development-guide.md   # This file
│   └── operations-guide.md    # Business operations reference
├── Makefile
├── go.mod
└── go.sum
```

---

## Mac/Linux Development Notes

If developing on a Mac or Linux machine:

- `make build` will compile but the result is not a Windows executable. Use
  `GOOS=windows GOARCH=amd64 go build ...` to cross-compile for Windows.
- `make logs` will not work (PowerShell-only).
- `make deploy-wp` and `make deploy-staging` will not work (WinSCP-only). Use
  `sftp` or `rsync` manually, or adapt the Makefile for your platform.
- Any code touching the Windows Registry (`registry.go`) will not compile without
  the `//go:build windows` build tag already in place — this is correct by design.
- All UI development (templates, static assets) and WordPress plugin development
  can be done on any platform. Testing requires Windows.

---

## Succession Notes

If you are picking this up after the original developer: welcome. Everything needed
to build, test, and deploy LogScene is in this repo. The Operations Guide
(`docs/operations-guide.md`) covers the business side — Paddle, WordPress, banking,
and annual renewals. Start there for context, then return here for the technical setup.

The Ed25519 private key needed to generate new license key batches is on a USB drive
stored securely by the owner. Contact the estate or successor for access.
