package sorter

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A file name pattern is any text with three codes in it, for example
// "{Name}-{Date}-{Time}" or "Wendy_{Date}_{Time}":
//
//	{Name}  the file's current name, without its extension
//	{Date}  the capture date, yyyymmdd
//	{Time}  the capture time, HHMMSS
//
// Codes are matched in any case; other text, braces included, is kept as
// typed. The extension is always the file's own.

// DefaultPattern is the naming Photo Sorter has always used.
const DefaultPattern = "{Name}-{Date}-{Time}"

var patternCode = regexp.MustCompile(`(?i)\{(name|date|time)\}`)

// namePattern is the pattern new names are built from; "" keeps names.
func (o Options) namePattern() string {
	if !o.Rename {
		return ""
	}
	if strings.TrimSpace(o.Pattern) == "" {
		return DefaultPattern
	}
	return strings.TrimSpace(o.Pattern)
}

// CheckPattern says what's wrong with a pattern, if anything.
func CheckPattern(p string) error {
	text := patternCode.ReplaceAllString(p, "")
	if i := strings.IndexAny(text, `\/:*?"<>|`); i >= 0 {
		return fmt.Errorf(`file names can't contain %c`, text[i])
	}
	for _, r := range text {
		if r < 32 {
			return fmt.Errorf("file names can't contain control characters")
		}
	}
	if len(p) > 200 {
		return fmt.Errorf("the file name pattern is too long")
	}
	return nil
}

// targetName is a file's new name under pattern. A name that already fits
// the pattern (with this file's own date and time, and perhaps a _N added
// for a clash) is kept, so renaming twice changes nothing.
func targetName(base string, d time.Time, pattern string) string {
	if pattern == "" {
		return base
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if fitsPattern(stem, d, pattern) {
		return base
	}
	name := patternCode.ReplaceAllStringFunc(pattern, func(c string) string {
		switch strings.ToLower(c) {
		case "{name}":
			return stem
		case "{date}":
			return d.Format("20060102")
		}
		return d.Format("150405")
	})
	name = strings.TrimRight(strings.TrimSpace(name), ". ") // Windows drops these anyway
	if name == "" {
		return base
	}
	return name + ext
}

func fitsPattern(stem string, d time.Time, pattern string) bool {
	var re strings.Builder
	re.WriteString(`(?i)^`)
	last := 0
	for _, m := range patternCode.FindAllStringIndex(pattern, -1) {
		re.WriteString(regexp.QuoteMeta(pattern[last:m[0]]))
		switch strings.ToLower(pattern[m[0]:m[1]]) {
		case "{name}":
			re.WriteString(`.+`)
		case "{date}":
			re.WriteString(d.Format("20060102"))
		default:
			re.WriteString(d.Format("150405"))
		}
		last = m[1]
	}
	re.WriteString(regexp.QuoteMeta(strings.TrimRight(strings.TrimSpace(pattern[last:]), ". ")))
	re.WriteString(`(_\d+)?$`)
	ok, _ := regexp.MatchString(re.String(), stem)
	return ok
}
