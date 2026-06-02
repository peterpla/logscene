# timelapse

A lightweight Go server that captures images from public webcam URLs on a
solar-aware schedule and assembles them into timelapse videos with ffmpeg.

Capture times are anchored to sunrise and sunset at each webcam's location,
with configurable additional shots evenly distributed through the day. A full
year of captures at 49 shots/day yields roughly 6 minutes of video at 24 fps.

---

## Table of Contents

- [Features](#features)
- [Prerequisites](#prerequisites)
- [Building](#building)
- [Configuration](#configuration)
- [Running](#running)
- [Webcam configuration (timelapse.json)](#webcam-configuration-timelapsejson)
- [API reference](#api-reference)
- [Rendering a timelapse video](#rendering-a-timelapse-video)
- [Development](#development)

---

## Features

- **Solar-aware scheduling** — first and last captures of the day are tied to
  sunrise/sunset (or offsets of ±30/60 min), automatically adjusting as days
  lengthen and shorten through the year
- **Flexible daily density** — 0–47 additional evenly-spaced shots between the
  first and last capture (49 shots/day at max ≈ one every 15 minutes)
- **Resilient capture loop** — graduated outage backoff: exponential retry for
  the first 24 h, hourly for 24–48 h, daily for 48 h–2 weeks, then
  auto-suspend with a prominent log message
- **Self-contained binary** — HTML templates and static assets are embedded
  via `go:embed`; no external files required at runtime
- **ffmpeg rendering** — `POST /render` assembles captured frames into an
  `.mp4` using the concat demuxer (works on Windows and Linux); supports
  optional date-range filtering and configurable frame rate
- **Rotating daily logs** — one log file per calendar day; readable via
  `GET /logs` without needing shell access to the host

---

## Prerequisites

| Requirement | Notes |
|---|---|
| Go 1.25+ | `go version` to check |
| [TimezoneDB API key](https://timezonedb.com/register) | Free tier is sufficient; used to look up webcam timezones |
| [ffmpeg](https://ffmpeg.org/download.html) on `PATH` | Required only for `POST /render` |

---

## Building

```powershell
# PowerShell (Windows)
$version   = git describe --tags --always --dirty
$builddate = Get-Date -Format "yyyy-MM-ddTHH:mm:ssZ"
go build -ldflags "-X main.Version=$version -X main.BuildDate=$builddate" -o timelapse.exe .
```

```bash
# Bash / Git Bash / Linux
make build
```

The `make build` target uses `git describe --tags` for the version and
`date -u` for the build timestamp. Building with plain `go build` (no
ldflags) produces a binary that reports `version=dev, build_date=unknown`.

---

## Configuration

All settings can be provided as **command-line flags** or **environment
variables**. Flags take priority over environment variables.

| Flag | Environment variable | Default | Description |
|---|---|---|---|
| `-tzdb` | `TIMELAPSE_TZDB` | *(required)* | [TimezoneDB](https://timezonedb.com) API key |
| `-base` | `TIMELAPSE_BASE` | `./captures` | Root directory where webcam image folders are created |
| `-path` | `TIMELAPSE_PATH` | `./` | Directory containing `timelapse.json` |
| `-logdir` | `TIMELAPSE_LOGDIR` | `./logs` | Directory for daily rotating log files |
| `-port` | `PORT` or `TIMELAPSE_PORT` | `8099` | HTTP listen port (`PORT` checked first for Cloud Run compatibility) |
| `-poll` | `TIMELAPSE_POLL` | `60` | Seconds between capture-due checks |
| `-storage` | `TIMELAPSE_STORAGE` | `local` | Storage backend: `local` (GCS and S3 not yet implemented) |

### Example (PowerShell)

```powershell
$env:TIMELAPSE_TZDB    = "your_api_key_here"
$env:TIMELAPSE_BASE    = "C:\Pictures\Timelapse"
$env:TIMELAPSE_PATH    = "C:\Pictures\Timelapse"
$env:TIMELAPSE_LOGDIR  = "C:\Pictures\Timelapse\logs"
.\timelapse.exe
```

---

## Running

On startup the server:
1. Reads `timelapse.json` and launches one capture goroutine per webcam
2. Prints `GET /info`, `GET /status`, and `GET /next` responses to stdout
   so the operator gets immediate confirmation even when logs are redirected
3. Listens for `SIGINT` (Ctrl-C) to shut down gracefully

To launch in a separate window (PowerShell):

```powershell
Start-Process powershell -ArgumentList "-NoExit", "-Command", "& 'C:\path\to\timelapse.exe'"
```

---

## Webcam configuration (timelapse.json)

Webcams are stored in `timelapse.json` in the directory specified by
`-path` / `TIMELAPSE_PATH`. The file is created and updated by `POST /new`
but can also be edited by hand (restart required to pick up manual changes).

```json
[
  {
    "name": "Kohm Yah-man-yeh",
    "webcamUrl": "https://example.com/webcam.jpg",
    "latitude": 40.437787,
    "longitude": -121.5360307,
    "firstSunrise": true,
    "firstSunrise30": false,
    "firstSunrise60": false,
    "firstTime": false,
    "firstTimeValue": "",
    "lastSunset": true,
    "lastSunset30": false,
    "lastSunset60": false,
    "lastTime": false,
    "lastTimeValue": "",
    "additional": 47,
    "folder": "kohm-yah-mah-nee",
    "webcamTZ": "America/Los_Angeles"
  }
]
```

### Schedule fields

**First capture of the day** — exactly one of these should be `true`:

| Field | Meaning |
|---|---|
| `firstSunrise` | At sunrise |
| `firstSunrise30` | 30 minutes after sunrise |
| `firstSunrise60` | 60 minutes after sunrise |
| `firstTime` | Fixed local time given by `firstTimeValue` (`"HH:MM"`) |

**Last capture of the day** — exactly one of these should be `true`:

| Field | Meaning |
|---|---|
| `lastSunset` | At sunset |
| `lastSunset30` | 30 minutes before sunset |
| `lastSunset60` | 60 minutes before sunset |
| `lastTime` | Fixed local time given by `lastTimeValue` (`"HH:MM"`) |

**`additional`** — integer 0–47. Shots evenly distributed between the first
and last capture. `additional: 47` yields 49 shots/day (≈ one every 15
minutes across a 12-hour day).

**`folder`** — subdirectory name under `TIMELAPSE_BASE` where images are
stored. Images are named `<webcam name> YYYYMMDDhhmmss.jpg`.

**`webcamTZ`** — IANA timezone name, cached automatically after the first
successful capture. Leave empty on first entry.

**`disabled`** — set to `true` to skip a webcam at startup without removing
it from the file.

---

## API reference

| Method | Path | Description |
|---|---|---|
| `GET` | `/info` | Build version, build date, Go version |
| `GET` | `/status` | Server health, webcam count, uptime |
| `GET` | `/next` | Webcam name and time of next scheduled capture |
| `GET` | `/logs[?n=N]` | Last N lines of today's log file (default 20) |
| `POST` | `/new` | Add a webcam (form-encoded, same fields as timelapse.json) |
| `POST` | `/render` | Render a timelapse video (JSON body, see below) |

### GET /info

```json
{
  "version": "v0.3.0",
  "build_date": "2026-06-02T13:58:57Z",
  "go_version": "go1.26.3"
}
```

### GET /status

```json
{ "status": "ok", "webcams": 5, "uptime": "3h22m15s" }
```

### GET /next

```json
{
  "webcam": "Kohm Yah-man-yeh",
  "next_capture": "2026-06-02T19:07:51Z",
  "next_capture_local": "2026-06-02T14:07:51-05:00",
  "in": "6m59s"
}
```

---

## Rendering a timelapse video

`POST /render` is asynchronous — it returns immediately and runs ffmpeg in
the background. Progress and errors appear in the log.

```powershell
Invoke-RestMethod -Method Post `
  -Uri "http://localhost:8099/render" `
  -ContentType "application/json" `
  -Body '{
    "folder": "kohm-yah-mah-nee",
    "output": "C:/Pictures/Timelapse/kohm-yah-mah-nee.mp4",
    "start": "2026-05-30",
    "end":   "2026-06-02",
    "fps":   24
  }'
```

| Field | Required | Description |
|---|---|---|
| `folder` | Yes | Webcam folder name under `TIMELAPSE_BASE` |
| `output` | Yes | Full path for the output `.mp4` file |
| `start` | No | `YYYY-MM-DD` — include only frames on or after this date |
| `end` | No | `YYYY-MM-DD` — include only frames on or before this date |
| `fps` | No | Frames per second (default: `24`) |

Omit `start` and `end` to render all captured frames for a webcam.

**Frame rate guidance for landscape timelapses:**

| fps | 1 year at 49 shots/day |
|---|---|
| 10 | ~30 minutes |
| 24 | ~12 minutes |
| 30 | ~10 minutes |

---

## Development

```bash
# Run all tests
go test ./...

# Run tests with verbose output
go test ./... -v

# Check test coverage
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out -o coverage.html
```

ffmpeg is not required to run the test suite. The render test exercises the
ffmpeg code path but skips a hard failure if ffmpeg is not on `PATH`.

---

## License

[MIT](LICENSE)
