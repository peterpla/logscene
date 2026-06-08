# Manual Test Checklist

Items here are not covered by automated tests and must be verified manually.
Run this checklist before any release that touches the affected areas.

---

## Render Modal (dashboard.html)

**Covered by automated browser tests** (`browser_test.go`, `TestBrowser_renderModal_*`
via chromedp/Edge). No manual verification required unless the browser test suite
is disabled or the modal JS is changed in a way not yet reflected in those tests.

To run: `go test -tags integration -run TestBrowser_renderModal ./...`

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

### Single-instance enforcement

- [ ] With the app running, launch a second instance. Second instance should exit
      immediately and the existing window should come to the foreground.
- [ ] If the existing window is minimized, launching a second instance should restore
      and focus it.
