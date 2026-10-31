//go:build windows

package main

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"photosorter/internal/server"
	"photosorter/internal/store"
)

func init() { runtime.LockOSThread() } // the window must live on the main thread

// runDefault opens the app in its own window (WebView2, built into Windows
// 11). Without WebView2 it falls back to the default browser.
func runDefault() {
	configDir := filepath.Join(os.Getenv("APPDATA"), "PhotoSorter")
	st, err := store.Open(configDir)
	if err != nil {
		fatal("Photo Sorter can't use its settings folder:\n" + configDir + "\n\n" + err.Error())
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal(err.Error())
	}
	url := "http://" + ln.Addr().String() + "/"
	// A windowed app has no console: send crash reports to a file instead.
	if f, err := os.OpenFile(filepath.Join(configDir, "crash.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666); err == nil {
		debug.SetCrashOutput(f, debug.CrashOptions{})
	}

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  filepath.Join(configDir, "webview2"),
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title: "Photo Sorter", Width: 1280, Height: 860, IconId: 1, Center: true,
		},
	})
	var hwnd uintptr
	host := server.Host{Open: openExplorer, Roots: drives, LocalOnly: true}
	host.PickFolder = func(start string) (string, error) { return pickFolder(hwnd, start) }
	if w == nil {
		host.Quit = func() { os.Exit(0) }
	}
	srv := server.New(st, host)
	go http.Serve(ln, srv.Handler())

	if w == nil {
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
		select {} // runs until Quit in the page
	}
	hwnd = uintptr(w.Window())
	watchTaskbar(hwnd, srv.Status)
	w.Navigate(url)
	w.Run()
	w.Destroy()
	// Closed mid-sort: stop after the current file so no half copy is left.
	srv.StopAndWait(60 * time.Second)
}

func openExplorer(path string) error {
	return exec.Command("explorer.exe", filepath.Clean(path)).Start()
}

// drives lists the drive letters for the built-in folder browser.
func drives() []string {
	var out []string
	mask, _ := windows.GetLogicalDrives()
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, string(rune('A'+i))+`:\`)
		}
	}
	return out
}

func fatal(msg string) {
	t, _ := windows.UTF16PtrFromString("Photo Sorter")
	m, _ := windows.UTF16PtrFromString(msg)
	windows.MessageBox(0, m, t, windows.MB_ICONERROR)
	os.Exit(1)
}
