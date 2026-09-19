package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tool scores destructive actions. It must not be able to perform one.
// This walks the WHOLE repository rather than trusting review to notice --
// not just internal/, which would miss cmd/sounding, the package that
// actually builds the clients and wires every other package together. A
// mutating call planted there is exactly as dangerous as one planted in
// internal/cluster itself, and a walker that only covers internal/ would
// let it ship silently.
func TestNoMutatingClientCallsAnywhere(t *testing.T) {
	banned := []string{".Delete(", ".DeleteCollection(", ".Create(", ".Update(", ".Patch("}
	root := filepath.Join("..", "..")
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// .git holds compressed object data, not source, and walking it
			// is both pointless and slow.
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, bad := range banned {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s calls %s -- this tool is read-only by construction", p, bad)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
