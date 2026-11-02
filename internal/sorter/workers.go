package sorter

// Workers is how many files are copied at the same time. SSDs, SD cards
// and network shares are fastest with several copies in flight; a spinning
// hard disk would thrash, so it gets one.
func Workers(o Options) int {
	if o.Parallel > 0 {
		return min(o.Parallel, 16)
	}
	return autoWorkers(append([]string{o.Dest}, o.Sources...), !o.QuickCheck)
}

// Speed describes what Auto picks for these folders, for the UI.
func Speed(o Options) (workers int, reason string) {
	if o.Parallel > 0 {
		return Workers(o), "set by you"
	}
	n := Workers(o)
	if n == 1 {
		return n, "a spinning hard disk is involved"
	}
	return n, "SSD, SD card or network"
}
