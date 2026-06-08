// Copyright (c) 2026 Peter Plamondon. All Rights Reserved.

//go:build windows

package main

import (
	"log"
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
		log.Printf("ensureSingleInstance: CreateMutex: %v", err)
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

// runUI opens a WebView2 window pointed at the local HTTP server and blocks
// until the window is closed or SIGINT is received. If the WebView2 runtime
// is not installed, it falls back to blocking on SIGINT and logs a message
// so the user can open the URL in a browser.
func runUI(port string) {
	w := webview.New(false)
	if w == nil {
		log.Printf("runUI: WebView2 runtime not available — open http://127.0.0.1:%s in a browser", port)
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
