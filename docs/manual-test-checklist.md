# Manual Test Checklist

Items here are not covered by automated tests and must be verified manually.
Run this checklist before any release that touches the affected areas.

---

## WebView2 Window (webview_windows.go)

### First launch

- [ ] Window titled "LogScene" opens automatically on `make run` — no browser required.
- [ ] Dashboard loads correctly with full Bootstrap + HTMX styling.
- [ ] **Watch for:** brief blank/error page before the server is ready. If seen, a
      pre-navigation delay in `runUI` is needed. If not seen, no fix required.

### Shutdown

- [x] Closing the window (X button) shuts the app down cleanly (no orphan process).
- [x] Ctrl-C in the terminal also closes the window and shuts down cleanly.
- **Known:** Ctrl-C produces a benign Chromium cleanup message in the terminal:
  `Failed to unregister class Chrome_WidgetWin_0. Error = 1411`
  This is a WebView2/Chromium internal cleanup race; the process exits correctly.

### Browser-mode fallback (WebView2 absent)

Requires a machine or VM where the WebView2 runtime is **not** installed.
A Windows 10 VM before WebView2 is added, or Windows Sandbox on a host
where WebView2 has not propagated into the sandbox, are suitable environments.
(Step 7 hardware testing — "Windows 10 VM (Hyper-V)" — is the planned gate.)

- [ ] Launch the app on a machine without WebView2. A native TaskDialog should
      appear immediately with the title "LogScene", the browser-mode message,
      and the loopback URL as a clickable hyperlink.
- [ ] Click the URL hyperlink in the dialog. The default browser should open to
      `http://127.0.0.1:<port>` and the dashboard should load.
- [ ] Click OK to dismiss the dialog. The app should remain running (SIGINT only).
- [ ] Verify `logscene-<date>.log` contains: `running in browser mode`.

### Single-instance enforcement

- [ ] With the app running, launch a second instance. Second instance should exit
      immediately and the existing window should come to the foreground.
- [ ] If the existing window is minimized, launching a second instance should restore
      and focus it.
