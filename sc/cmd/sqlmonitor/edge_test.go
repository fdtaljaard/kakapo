package main

import (
	"path/filepath"
	"reflect"
	"testing"
)

// Edge is looked for where the registry says it is first, then the per-user install, then
// Program Files; quotes, blanks and repeats are dropped.
func TestEdgeCandidates(t *testing.T) {
	pf86 := filepath.Join(`C:\Program Files (x86)`, `Microsoft\Edge\Application\msedge.exe`)
	got := edgeCandidates([]string{`"` + pf86 + `"`, " ", `D:\Apps\Edge\msedge.exe`}, `C:\Users\u\AppData\Local`, `C:\Program Files (x86)`, `C:\Program Files`)
	want := []string{pf86, `D:\Apps\Edge\msedge.exe`,
		filepath.Join(`C:\Users\u\AppData\Local`, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(`C:\Program Files`, `Microsoft\Edge\Application\msedge.exe`)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("candidates\n got %q\nwant %q", got, want)
	}
	if got := edgeCandidates(nil, "", "", ""); len(got) != 0 {
		t.Errorf("nothing known: %q", got)
	}
}
