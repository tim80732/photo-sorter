//go:build windows

package sorter

import (
	"hash/maphash"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// longPath adds the \\?\ prefix that raw Win32 calls need for paths over MAX_PATH.
func longPath(p string) string {
	if len(p) < 248 || strings.HasPrefix(p, `\\?\`) {
		return p
	}
	if strings.HasPrefix(p, `\\`) {
		return `\\?\UNC\` + p[2:]
	}
	return `\\?\` + p
}

// renameNoReplace moves a file without ever overwriting the target.
func renameNoReplace(from, to string) error {
	f, err := windows.UTF16PtrFromString(longPath(from))
	if err != nil {
		return err
	}
	t, err := windows.UTF16PtrFromString(longPath(to))
	if err != nil {
		return err
	}
	switch err := windows.MoveFileEx(f, t, 0); err {
	case nil:
		return nil
	case windows.ERROR_ALREADY_EXISTS, windows.ERROR_FILE_EXISTS:
		return errExists
	case windows.ERROR_NOT_SAME_DEVICE:
		return errCrossDevice
	default:
		return err
	}
}

// copyTimes gives dst the created, accessed and modified times of src.
func copyTimes(dst string, src os.FileInfo) error {
	if err := os.Chtimes(dst, src.ModTime(), src.ModTime()); err != nil {
		return err
	}
	d, ok := src.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return nil
	}
	p, err := windows.UTF16PtrFromString(longPath(dst))
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	c := windows.Filetime(d.CreationTime)
	a := windows.Filetime(d.LastAccessTime)
	w := windows.Filetime(d.LastWriteTime)
	return windows.SetFileTime(h, &c, &a, &w)
}

// checksumFromDisk reads the file with the Windows file cache bypassed
// (FILE_FLAG_NO_BUFFERING), so the data comes from the disk itself.
func checksumFromDisk(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(longPath(path))
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_NO_BUFFERING|windows.FILE_FLAG_SEQUENTIAL_SCAN, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(h)
	b := alignedPool.Get().(*[]byte)
	defer alignedPool.Put(b)
	var sum maphash.Hash
	sum.SetSeed(checkSeed)
	for {
		var n uint32
		if err := windows.ReadFile(h, *b, &n, nil); err != nil {
			return 0, err
		}
		if n == 0 {
			return sum.Sum64(), nil
		}
		sum.Write((*b)[:n])
	}
}

func finishNewFile(string) {}

func mkdirShared(p string) error { return os.Mkdir(p, 0o777) }

// removeFile deletes a file, clearing the read-only flag if that's in the way.
func removeFile(p string) error {
	err := os.Remove(p)
	if err != nil && !os.IsNotExist(err) {
		os.Chmod(p, 0o666)
		err = os.Remove(p)
	}
	return err
}
