//go:build windows

package main

import (
	"errors"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The standard Windows folder picker (IFileOpenDialog with FOS_PICKFOLDERS),
// called through its COM vtable directly.

var (
	ole32                               = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance                = ole32.NewProc("CoCreateInstance")
	shell32                             = windows.NewLazySystemDLL("shell32.dll")
	procSHCreateItemFromParsing         = shell32.NewProc("SHCreateItemFromParsingName")
	clsidFileOpenDialog                 = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog                  = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	iidIShellItem                       = windows.GUID{Data1: 0x43826D1E, Data2: 0xE718, Data3: 0x42EE, Data4: [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
	errCancelledHR              uintptr = 0x800704C7 // HRESULT_FROM_WIN32(ERROR_CANCELLED)
)

// vtable slots
const (
	slotRelease        = 2
	slotShow           = 3
	slotSetOptions     = 9
	slotGetOptions     = 10
	slotSetFolder      = 12
	slotSetTitle       = 17
	slotGetResult      = 20
	slotGetDisplayName = 5 // IShellItem
)

const (
	fosPickFolders     = 0x20
	fosForceFileSystem = 0x40
	fosPathMustExist   = 0x800
	sigdnFileSysPath   = 0x80058000
)

// call invokes method n of a COM object.
func call(obj unsafe.Pointer, n int, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, n*int(unsafe.Sizeof(uintptr(0)))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return r
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

// pickFolder shows the folder dialog and returns the chosen path, or "" if
// the user cancelled.
func pickFolder(owner uintptr, start string) (string, error) {
	runtime.LockOSThread() // COM objects belong to the thread that made them
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil {
		defer windows.CoUninitialize()
	}

	var dlg unsafe.Pointer
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, windows.CLSCTX_INPROC_SERVER,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&dlg)))
	if failed(hr) || dlg == nil {
		return "", errors.New("the Windows folder dialog isn't available")
	}
	defer call(dlg, slotRelease)

	var opts uint32
	call(dlg, slotGetOptions, uintptr(unsafe.Pointer(&opts)))
	call(dlg, slotSetOptions, uintptr(opts|fosPickFolders|fosForceFileSystem|fosPathMustExist))
	if t, err := windows.UTF16PtrFromString("Choose a folder"); err == nil {
		call(dlg, slotSetTitle, uintptr(unsafe.Pointer(t)))
	}
	if start != "" {
		if p, err := windows.UTF16PtrFromString(start); err == nil {
			var item unsafe.Pointer
			hr, _, _ := procSHCreateItemFromParsing.Call(uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&iidIShellItem)), uintptr(unsafe.Pointer(&item)))
			if !failed(hr) && item != nil {
				call(dlg, slotSetFolder, uintptr(item))
				call(item, slotRelease)
			}
		}
	}

	hr = call(dlg, slotShow, owner)
	if hr == errCancelledHR {
		return "", nil
	}
	if failed(hr) {
		return "", syscall.Errno(hr)
	}
	var item unsafe.Pointer
	if hr := call(dlg, slotGetResult, uintptr(unsafe.Pointer(&item))); failed(hr) || item == nil {
		return "", errors.New("couldn't read the chosen folder")
	}
	defer call(item, slotRelease)
	var name *uint16
	if hr := call(item, slotGetDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&name))); failed(hr) || name == nil {
		return "", errors.New("that location isn't a normal folder")
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(name))
	return windows.UTF16PtrToString(name), nil
}
