package sorter

import (
	"path/filepath"
	"strings"
)

// Cat is a destination folder type.
type Cat string

const (
	CatJPG     Cat = "JPG"
	CatRAW     Cat = "RAW"
	CatMOV     Cat = "MOV"
	CatSidecar Cat = "SIDECAR"
)

// Cats are the folders created inside every month folder, in display order.
var Cats = []Cat{CatJPG, CatRAW, CatMOV}

// File kinds found while scanning.
const (
	kindMedia   = "media"
	kindSidecar = "sidecar"
	kindSystem  = "system"
	kindUnknown = "unknown"
)

var extCat = map[string]Cat{}

func init() {
	for _, e := range strings.Fields("jpg jpeg jpe heic heif hif") {
		extCat[e] = CatJPG
	}
	// Every common camera RAW format.
	for _, e := range strings.Fields(`cr3 cr2 crw nef nrw raf arw srf sr2 dng orf ori rw2 raw rwl
		pef 3fr fff iiq x3f srw erf mef mos kdc dcr mrw gpr`) {
		extCat[e] = CatRAW
	}
	for _, e := range strings.Fields("mp4 mov m4v avi mts m2ts 3gp crm mxf") {
		extCat[e] = CatMOV
	}
}

var sidecarExt = map[string]bool{"xmp": true, "pp3": true, "dop": true}

// Files cameras and operating systems leave on cards: indexes, thumbnails,
// low-res proxies, clip XML, AVCHD navigation files.
var systemExt = map[string]bool{
	"ctg": true, "thm": true, "lrv": true, "lrf": true, "xml": true, "mpl": true,
	"cpi": true, "bdm": true, "dat": true, "ind": true, "part": true,
}

var systemNames = map[string]bool{".ds_store": true, "thumbs.db": true, "desktop.ini": true, ".localized": true}

// Folders skipped entirely while scanning (never shown).
var skipDirs = map[string]bool{
	"$recycle.bin": true, "system volume information": true, ".trashes": true,
	".spotlight-v100": true, ".fseventsd": true, ".temporaryitems": true, "@eadir": true,
}

func extOf(name string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
}

// classify returns what a file is, and its folder for media.
func classify(name string) (kind string, cat Cat) {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "._") || systemNames[lower] || strings.HasPrefix(lower, ".photosorter-") {
		return kindSystem, ""
	}
	ext := extOf(name)
	if c, ok := extCat[ext]; ok {
		return kindMedia, c
	}
	if sidecarExt[ext] {
		return kindSidecar, CatSidecar
	}
	if systemExt[ext] {
		return kindSystem, ""
	}
	return kindUnknown, ""
}

// IsMediaName reports whether a file name would be sorted (used for quick counts).
func IsMediaName(name string) bool {
	k, _ := classify(name)
	return k == kindMedia
}

// SkipDir reports whether a folder is ignored while scanning.
func SkipDir(name string) bool { return skipDirs[strings.ToLower(name)] }
