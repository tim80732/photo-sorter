//go:build windows

package main

import (
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"photosorter/internal/server"
)

// Taskbar progress, the same green/red bar Explorer shows on its taskbar
// icon while copying (ITaskbarList3).

var (
	clsidTaskbarList = windows.GUID{Data1: 0x56FDF344, Data2: 0xFD6D, Data3: 0x11D0, Data4: [8]byte{0x95, 0x8A, 0x00, 0x60, 0x97, 0xC9, 0xA0, 0x90}}
	iidITaskbarList3 = windows.GUID{Data1: 0xEA1AFB91, Data2: 0x9E28, Data3: 0x4B86, Data4: [8]byte{0x90, 0xE9, 0x9E, 0x9F, 0x8A, 0x5E, 0xEF, 0xAF}}

	user32              = windows.NewLazySystemDLL("user32.dll")
	procFlashWindowEx   = user32.NewProc("FlashWindowEx")
	procGetForegroundWd = user32.NewProc("GetForegroundWindow")
)

const (
	slotHrInit           = 3
	slotSetProgressValue = 9
	slotSetProgressState = 10

	tbpfNoProgress    = 0
	tbpfIndeterminate = 0x1
	tbpfNormal        = 0x2 // green
	tbpfError         = 0x4 // red
)

// watchTaskbar mirrors the running scan or sort on the taskbar icon until
// the process ends, and flashes the icon when a sort finishes in the
// background.
func watchTaskbar(hwnd uintptr, status func() server.Status) {
	go func() {
		runtime.LockOSThread()
		if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil {
			return
		}
		var tb unsafe.Pointer
		hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidTaskbarList)), 0, windows.CLSCTX_INPROC_SERVER,
			uintptr(unsafe.Pointer(&iidITaskbarList3)), uintptr(unsafe.Pointer(&tb)))
		if failed(hr) || tb == nil || failed(call(tb, slotHrInit)) {
			return
		}
		state := -1
		setState := func(s int) {
			if s != state {
				call(tb, slotSetProgressState, hwnd, uintptr(s))
				state = s
			}
		}
		wasSorting := false
		for range time.Tick(400 * time.Millisecond) {
			st := status()
			switch {
			case st.Running && st.Kind == "scan":
				setState(tbpfIndeterminate)
			case st.Running && st.Kind == "run":
				wasSorting = true
				p := st.Progress
				if p.Counts["failed"] > 0 {
					setState(tbpfError)
				} else {
					setState(tbpfNormal)
				}
				done, total := uint64(p.BytesDone), uint64(p.BytesTotal)
				if total == 0 {
					done, total = uint64(p.Done), uint64(max(p.Total, 1))
				}
				call(tb, slotSetProgressValue, hwnd, uintptr(done), uintptr(total))
			default:
				setState(tbpfNoProgress)
				if wasSorting {
					wasSorting = false
					flashIfBackground(hwnd)
				}
			}
		}
	}()
}

// flashIfBackground flashes the taskbar button until the window is brought
// to the front (only if it isn't already).
func flashIfBackground(hwnd uintptr) {
	if fg, _, _ := procGetForegroundWd.Call(); fg == hwnd {
		return
	}
	info := struct {
		Size    uint32
		Hwnd    uintptr
		Flags   uint32
		Count   uint32
		Timeout uint32
	}{Hwnd: hwnd, Flags: 0x2 | 0xC} // FLASHW_TRAY | FLASHW_TIMERNOFG
	info.Size = uint32(unsafe.Sizeof(info))
	procFlashWindowEx.Call(uintptr(unsafe.Pointer(&info)))
}
