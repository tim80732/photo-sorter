//go:build linux

package sorter

import (
	"errors"
	"hash/maphash"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace moves a file without ever overwriting the target.
// Unraid's shfs may not support RENAME_NOREPLACE, so fall back to a check.
func renameNoReplace(from, to string) error {
	err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EEXIST):
		return errExists
	case errors.Is(err, unix.EXDEV):
		return errCrossDevice
	case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EOPNOTSUPP):
		if _, err := os.Lstat(to); err == nil {
			return errExists
		}
		err = os.Rename(from, to)
		if errors.Is(err, unix.EXDEV) {
			return errCrossDevice
		}
		return err
	}
	return err
}

func copyTimes(dst string, src os.FileInfo) error {
	return os.Chtimes(dst, src.ModTime(), src.ModTime())
}

// checksumFromDisk reads the file with the page cache bypassed (O_DIRECT),
// so the data comes from the disk. Unraid's /mnt/user shares pass O_DIRECT
// on to the pool or array disk underneath. File systems that refuse it
// (tmpfs) get the copy dropped from the page cache and read normally; the
// copy was flushed (fsync) first, so its cached pages are clean.
func checksumFromDisk(path string) (uint64, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_DIRECT, 0)
	if err != nil {
		if f, err = os.Open(path); err != nil {
			return 0, err
		}
		defer f.Close()
		unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
		return checksumReader(f)
	}
	defer f.Close()
	b := alignedPool.Get().(*[]byte)
	defer alignedPool.Put(b)
	var sum maphash.Hash
	sum.SetSeed(checkSeed)
	for {
		n, err := f.Read(*b)
		sum.Write((*b)[:n])
		if err == io.EOF {
			return sum.Sum64(), nil
		}
		if err != nil {
			return 0, err
		}
	}
}

// Unraid shares expect rw for everyone (files 666, folders 777), whatever
// the container's umask is.
func finishNewFile(p string) { os.Chmod(p, 0o666) }

func mkdirShared(p string) error {
	if err := os.Mkdir(p, 0o777); err != nil {
		return err
	}
	return os.Chmod(p, 0o777)
}

func removeFile(p string) error { return os.Remove(p) }
