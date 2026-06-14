// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

//go:build windows

package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"unsafe"

	webview "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procFindWindowW         = user32.NewProc("FindWindowW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")

	comctl32               = windows.NewLazySystemDLL("comctl32.dll")
	procTaskDialogIndirect = comctl32.NewProc("TaskDialogIndirect")

	shell32           = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteW = shell32.NewProc("ShellExecuteW")
)

const (
	appMutexName = "Global\\LogScene"
	swRestore    = 9
)

// appMutex holds the named mutex for the lifetime of the process so the OS
// keeps it open and other instances can detect us.
var appMutex windows.Handle

// ensureSingleInstance returns true if this is the only running instance.
// If another instance is already running, it brings that window to the
// foreground and returns false; the caller should exit without starting.
func ensureSingleInstance() bool {
	h, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(appMutexName))
	if err != nil {
		if err == windows.ERROR_ALREADY_EXISTS {
			windows.CloseHandle(h)
			bringExistingWindowToFront()
			return false
		}
		slog.Debug("ensureSingleInstance: CreateMutex failed — single-instance guard bypassed",
			"failure_class", fcInternalError,
			"error", err)
		return true // unexpected error; let this instance run
	}
	appMutex = h
	return true
}

func bringExistingWindowToFront() {
	title, _ := syscall.UTF16PtrFromString("LogScene")
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(title)))
	if hwnd != 0 {
		procShowWindow.Call(hwnd, swRestore)
		procSetForegroundWindow.Call(hwnd)
	}
}

const (
	tdfEnableHyperlinks = uint32(0x0001)
	tdcbfOKButton       = uint32(0x0001)
	tdnHyperlinkClicked = uintptr(3)
)

// taskDialogConfig mirrors TASKDIALOGCONFIG (Windows SDK) on 64-bit.
// Go adds implicit padding after cbSize, cButtons, and nDefaultRadioButton
// to match the C struct layout (verified: sizeof = 176 bytes).
type taskDialogConfig struct {
	cbSize             uint32
	hwndParent         uintptr
	hInstance          uintptr
	dwFlags            uint32
	dwCommonButtons    uint32
	pszWindowTitle     *uint16
	hMainIcon          uintptr // union: HICON or PCWSTR pszMainIcon
	pszMainInstruction *uint16
	pszContent         *uint16
	cButtons           uint32
	pButtons           uintptr
	nDefaultButton     int32
	cRadioButtons      uint32
	pRadioButtons      uintptr
	nDefaultRadioButton         int32
	pszVerificationText         *uint16
	pszExpandedInformation      *uint16
	pszExpandedControlText      *uint16
	pszCollapsedControlText     *uint16
	hFooterIcon                 uintptr // union: HICON or PCWSTR pszFooterIcon
	pszFooter                   *uint16
	pfCallback                  uintptr
	lpCallbackData              uintptr
	cxWidth                     uint32
}

// showBrowserModeDialog shows a native TaskDialog when WebView2 is unavailable.
// The URL in the content is a clickable hyperlink that opens the default browser.
func showBrowserModeDialog(port string) {
	url := "http://127.0.0.1:" + port
	urlUTF16, _ := windows.UTF16PtrFromString(url)
	openVerb, _ := windows.UTF16PtrFromString("open")

	cb := windows.NewCallback(func(_, msg, _, _, _ uintptr) uintptr {
		if msg == tdnHyperlinkClicked {
			procShellExecuteW.Call(0, uintptr(unsafe.Pointer(openVerb)), uintptr(unsafe.Pointer(urlUTF16)), 0, 0, 1)
		}
		return 0 // S_OK
	})

	title, _ := windows.UTF16PtrFromString("LogScene")
	instruction, _ := windows.UTF16PtrFromString("LogScene is running in browser mode — all features are available.")
	content, _ := windows.UTF16PtrFromString(
		"Open <A HREF=\"" + url + "\">" + url + "</A> in your browser to use LogScene.\n" +
			"Keep this window open — closing it will stop LogScene.")

	cfg := taskDialogConfig{
		dwFlags:            tdfEnableHyperlinks,
		dwCommonButtons:    tdcbfOKButton,
		pszWindowTitle:     title,
		pszMainInstruction: instruction,
		pszContent:         content,
		pfCallback:         cb,
	}
	cfg.cbSize = uint32(unsafe.Sizeof(cfg))

	procTaskDialogIndirect.Call(uintptr(unsafe.Pointer(&cfg)), 0, 0, 0)
}

// runUI opens a WebView2 window pointed at the local HTTP server and blocks
// until the window is closed or SIGINT is received. If the WebView2 runtime
// is not installed, it falls back to blocking on SIGINT and logs a message
// so the user can open the URL in a browser.
func runUI(port string) {
	w := webview.New(false)
	if w == nil {
		url := fmt.Sprintf("http://127.0.0.1:%s", port)
		slog.Info("running in browser mode — WebView2 runtime not available", "url", url)
		slog.Debug("webview.New returned nil — WebView2 runtime not installed; falling back to browser mode", "url", url)
		showBrowserModeDialog(port)
		// TODO Step 6i: add notification center entry for browser-mode fallback
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		<-c
		signal.Stop(c)
		return
	}
	defer w.Destroy()

	w.SetTitle("LogScene")
	w.SetSize(1280, 800, webview.HintNone)
	w.Init("document.addEventListener('contextmenu', function(e) { e.preventDefault(); });")
	w.Navigate("http://127.0.0.1:" + port)

	// Allow Ctrl-C from the terminal to close the window cleanly.
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		signal.Stop(c)
		w.Terminate()
	}()

	w.Run()
}
