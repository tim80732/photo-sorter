//go:build !windows

package sorter

// autoWorkers on the server: Unraid's array is spinning disks behind a
// parity disk, where many parallel writes slow everything down. Moves within
// a share are renames anyway, so this only matters for copies.
func autoWorkers([]string) int { return 2 }
