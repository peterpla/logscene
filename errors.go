// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

package main

// failure_class values for slog.Debug failure entries.
// Every slog.Debug call for a failure must include one of these as the
// "failure_class" attr so support bundles and log analysis can group errors
// by category without string matching on message text.
const (
	fcUnreachable     = "unreachable"     // camera/endpoint not responding
	fcNetworkAPI      = "network/API"     // network call to external API failed
	fcFilesystem      = "filesystem"      // filesystem read or write failed
	fcConfigParse     = "config_parse"    // config file could not be parsed
	fcConfigInvalid   = "config_invalid"  // config file parsed but failed validation
	fcRegistry        = "registry"        // Windows Registry read or write failed
	fcInternalError   = "internal_error"  // should never occur in production
	fcRenderNoFrames      = "render_no_frames"      // no .jpg files match the date filter
	fcRenderFFmpegMissing = "render_ffmpeg_missing"  // ffmpeg binary not found on PATH
	fcRenderCodecMissing  = "render_codec_missing"   // libx264 encoder not in ffmpeg build
	fcRenderDiskFull      = "render_disk_full"       // no space left on device writing output
	fcRenderPermission    = "render_permission"      // permission denied writing output
	fcRenderCanceled      = "render_canceled"        // context canceled (app shutdown during render)
	fcRenderFFmpegError   = "render_ffmpeg_error"    // unclassified ffmpeg failure
	fcRenderInternal      = "render_internal"        // unexpected internal error (ReadDir, temp file)
	fcMalformedTZ     = "malformed_timezone" // time.LoadLocation failed on a timezone string
)
