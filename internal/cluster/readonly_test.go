package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tool scores destructive actions. It must not be able to perform one.
// This walks our own source rather than trusting review to notice.
func TestNoMutatingClientCallsAnywhere(t *testing.T) {
	banned := []string{".Delete(", ".DeleteCollection(", ".Create(", ".Update(", ".Patch("}
	root := filepath.Join("..", "..", "internal")
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
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
