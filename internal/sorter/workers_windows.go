//go:build windows

package sorter

import (
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// autoWorkers: 1 if any local folder is on a spinning disk, else 4. Reading
// each copy back from the disk holds a worker until its copy is flushed and
// re-read, so with that check on, local flash gets 8 to keep the drives busy
// (NVMe to NVMe, 9.3 GB: 8.5 s with 8 instead of 9.4 s with 4).
func autoWorkers(paths []string, diskCheck bool) int {
	remote := false
	for _, p := range paths {
		if spinning(p) {
			return 1
		}
		remote = remote || isRemote(p)
	}
	if diskCheck && !remote {
		return 8
	}
	return 4
}

// isRemote reports a network path: UNC or a mapped network drive.
func isRemote(path string) bool {
	abs, _ := filepath.Abs(path)
	vol := filepath.VolumeName(abs)
	if len(vol) != 2 || vol[1] != ':' {
		return true
	}
	root, _ := windows.UTF16PtrFromString(vol + `\`)
	return windows.GetDriveType(root) == windows.DRIVE_REMOTE
}

// spinning asks the drive whether it has a seek penalty (a hard disk).
// Network paths and drives that don't answer (most SD readers) count as
// flash.
func spinning(path string) bool {
	if isRemote(path) {
		return false
	}
	abs, _ := filepath.Abs(path)
	vol := filepath.VolumeName(abs)
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
