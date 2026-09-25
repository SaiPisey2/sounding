package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreOrderingIsWritten(t *testing.T) {
	dir := t.TempDir()
	if err := writeOrdering(dir, []string{"a.yaml", "b.yaml"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "restore.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Index(s, "a.yaml") > strings.Index(s, "b.yaml") {
		t.Error("restore.sh must apply in the order given")
	}
	if !strings.Contains(s, "set -e") {
		t.Error("restore.sh must stop on the first failure rather than press on")
	}
}

// The bundle promises objects, not data. A reader who applies it must be told
// what will not come back, in the file itself -- not only in terminal output
// they may never have seen.
func TestExclusionsAreRecordedInTheBundle(t *testing.T) {
	dir := t.TempDir()
	if err := writeExclusions(dir, []string{"pv/pv-1 reclaimPolicy=Delete: data is not recoverable"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "NOT-RESTORED.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "pv-1") {
		t.Error("exclusions file must name what cannot be restored")
	}
}

// A filename is built from user-controlled object names. A name containing a
// path separator must not escape the snapshot directory.
func TestFilenameCannotEscapeTheDirectory(t *testing.T) {
	for _, name := range []string{"../evil", "a/b", "..", "."} {
		got := safeFilename("pods", name)
		if strings.Contains(got, "/") || strings.Contains(got, "..") {
			t.Errorf("safeFilename(%q) = %q, which escapes", name, got)
		}
	}
}
