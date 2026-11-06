package sorter

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTargetName(t *testing.T) {
	d := time.Date(2026, 5, 3, 10, 15, 22, 0, time.Local)
	cases := []struct{ pattern, base, want string }{
		{"", "DSC_0412.JPG", "DSC_0412.JPG"},
		{DefaultPattern, "DSC_0412.JPG", "DSC_0412-20260503-101522.JPG"},
		{DefaultPattern, "DSC_0412-20260503-101522.JPG", "DSC_0412-20260503-101522.JPG"},   // already fits
		{DefaultPattern, "DSC_0412-20260503-101522_1.JPG", "DSC_0412-20260503-101522_1.JPG"}, // clash number
		{DefaultPattern, "dsc_0412-20260503-101522.jpg", "dsc_0412-20260503-101522.jpg"},     // any case
		{DefaultPattern, "DSC_0412-20260503-999999.JPG", "DSC_0412-20260503-999999-20260503-101522.JPG"},
		{"{Date}_{Time}_{Name}", "DSC_0412.NEF", "20260503_101522_DSC_0412.NEF"},
		{"{date}_{time}_{name}", "20260503_101522_DSC_0412.NEF", "20260503_101522_DSC_0412.NEF"},
		{"Wendy-{Name}", "DSC_0412.JPG", "Wendy-DSC_0412.JPG"},
		{"Wendy-{Name}", "Wendy-DSC_0412.JPG", "Wendy-DSC_0412.JPG"},
		{"Wendy {Date} {Time}", "DSC_0412.JPG", "Wendy 20260503 101522.JPG"},
		{"Trip (1) {Name}", "DSC_0412.JPG", "Trip (1) DSC_0412.JPG"},
		{"Trip (1) {Name}", "Trip (1) DSC_0412.JPG", "Trip (1) DSC_0412.JPG"},
		{"{Name}", "DSC_0412.JPG", "DSC_0412.JPG"},
		{"{trip}-{Name}", "DSC_0412.JPG", "{trip}-DSC_0412.JPG"},
		{"Wendy.", "DSC_0412.JPG", "Wendy.JPG"},
		{"相片-{Date}", "DSC_0412.JPG", "相片-20260503.JPG"},
	}
	for _, c := range cases {
		if got := targetName(c.base, d, c.pattern); got != c.want {
			t.Errorf("%q on %q: got %q, want %q", c.pattern, c.base, got, c.want)
		}
	}
}

func TestCheckPattern(t *testing.T) {
	for _, ok := range []string{"", DefaultPattern, "Wendy {Date}", "相片_{Name}", "{trip}-{Name}"} {
		if err := CheckPattern(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"a/b", `a\b`, "a:b", "a*", "a?", `"a"`, "<a>", "a|b", "a\tb"} {
		if CheckPattern(bad) == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// A Rename with a pattern names files by it, and a second run leaves them.
func TestTidyPattern(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "202605", "JPG", "DSC_0412.JPG")
	os.MkdirAll(filepath.Dir(p), 0o777)
	os.WriteFile(p, []byte("photo"), 0o666)
	when := time.Date(2026, 5, 3, 10, 15, 22, 0, time.Local)
	os.Chtimes(p, when, when)

	opt := Options{Dest: root, Tidy: true, JPG: true, Rename: true, Pattern: "{Date}_{Time}_{Name}"}
	p1, err := BuildPlan(context.Background(), opt, NewProgress())
	if err != nil {
		t.Fatal(err)
	}
	if got := p1.Items[0].Dest; got != "202605/JPG/20260503_101522_DSC_0412.JPG" {
		t.Fatalf("plan: %s", got)
	}
	Run(context.Background(), p1, NewProgress())
	p2, _ := BuildPlan(context.Background(), opt, NewProgress())
	if n, _ := p2.ToCopy(); n != 0 {
		t.Errorf("second run would rename again")
	}
	keep := opt
	keep.Rename = false
	p3, _ := BuildPlan(context.Background(), keep, NewProgress())
	if n, _ := p3.ToCopy(); n != 0 {
		t.Errorf("keeping names should leave the file in place")
	}
}
