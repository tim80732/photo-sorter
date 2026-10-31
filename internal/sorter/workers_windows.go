//go:build windows

package sorter

import (
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// autoWorkers: 1 if any local folder is on a spinning disk, else 4.
func autoWorkers(paths []string) int {
	for _, p := range paths {
		if spinning(p) {
			return 1
		}
	}
	return 4
}

// spinning asks the drive whether it has a seek penalty (a hard disk).
// Network paths and drives that don't answer (most SD readers) count as
// flash.
func spinning(path string) bool {
	abs, _ := filepath.Abs(path)
	vol := filepath.VolumeName(abs)
	if len(vol) != 2 || vol[1] != ':' {
		return false // UNC path: network
	}
	root, _ := windows.UTF16PtrFromString(vol + `\`)
	if windows.GetDriveType(root) == windows.DRIVE_REMOTE {
		return false
	}
	dev, _ := windows.UTF16PtrFromString(`\\.\` + strings.ToUpper(vol))
	h, err := windows.CreateFile(dev, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	const ioctlStorageQueryProperty = 0x2D1400
	query := struct {
		PropertyID, QueryType uint32
		Extra                 [4]byte
	}{PropertyID: 7} // StorageDeviceSeekPenaltyProperty, PropertyStandardQuery
	var desc struct {
		Version, Size     uint32
		IncursSeekPenalty byte
		_                 [3]byte
	}
	var n uint32
	err = windows.DeviceIoControl(h, ioctlStorageQueryProperty, (*byte)(unsafe.Pointer(&query)), uint32(unsafe.Sizeof(query)),
		(*byte)(unsafe.Pointer(&desc)), uint32(unsafe.Sizeof(desc)), &n, nil)
	return err == nil && n >= 9 && desc.IncursSeekPenalty != 0
}
