// Package snapshot writes the undo plan: the real manifests of every object
// a mutation would destroy, plus an ordering to restore them in. No shipping
// tool synthesises a compensating action for an arbitrary infrastructure
// mutation before running it, so a concrete bundle of what was actually
// there is what this package produces instead of a guessed reverse command.
//
// Whether a snapshot was taken must never leak back into classification: the
// class this tool reports is computed from whether an undo is *possible*,
// not from whether this package happened to run. This package only writes
// files and reports what it wrote.
package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/SaiPisey2/sounding/internal/cascade"
	"github.com/SaiPisey2/sounding/internal/cluster"
	"github.com/SaiPisey2/sounding/internal/model"
)

// unsafeChar matches anything that is not safe to carry into a filename
// unescaped. Object names are chosen by whoever created the object, not by
// this tool, so this is the boundary that keeps one from ever being read as
// a path component.
var unsafeChar = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// dotRun matches two or more consecutive dots. A single '.' is in the
// allowed character set, which is the trap: replacing "/" in "../evil"
// leaves the two dots untouched and adjacent, so the traversal token
// reappears right next to whatever replaced the slash. Only collapsing runs
// of dots -- after the character replacement, and again once the extension
// is appended -- removes it for good.
var dotRun = regexp.MustCompile(`\.{2,}`)

// safeFilename turns a resource and a user-controlled object name into a
// filename that cannot carry a directory-traversal sequence. Every
// character outside [A-Za-z0-9._-] becomes '_' first; the result then has
// any run of dots collapsed and any leading or trailing dot trimmed, so a
// name that was nothing but dots and slashes (".", "..", or something that
// reduces to one of them) can't reintroduce ".." once the "-" and ".json"
// around it are added back.
func safeFilename(resource, name string) string {
	res := unsafeChar.ReplaceAllString(resource, "_")
	safe := unsafeChar.ReplaceAllString(name, "_")
	safe = dotRun.ReplaceAllString(safe, "_")
	safe = strings.Trim(safe, ".")
	if safe == "" {
		safe = "obj"
	}
	return fmt.Sprintf("%s-%s.json", res, safe)
}

// writeOrdering writes restore.sh. It begins "#!/bin/sh" and "set -e" so
// that applying the bundle stops at the first failure instead of pressing on
// into a partially-restored ownership graph -- an owner that failed to apply
// but whose children get created anyway is a worse state than stopping.
func writeOrdering(dir string, files []string) error {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("set -e\n")
	for _, f := range files {
		b.WriteString("kubectl apply -f " + f + "\n")
	}
	return os.WriteFile(filepath.Join(dir, "restore.sh"), []byte(b.String()), 0o755)
}

// writeExclusions writes NOT-RESTORED.txt. The bundle promises objects, not
// data: a reader who applies restore.sh must be told what will not come
// back in the bundle itself, not only in terminal output from the run that
// produced it, which they may never have seen.
func writeExclusions(dir string, excluded []string) error {
	var b strings.Builder
	b.WriteString("These were not captured and will not be restored by this bundle:\n\n")
	if len(excluded) == 0 {
		b.WriteString("(none)\n")
	}
	for _, e := range excluded {
		b.WriteString("- " + e + "\n")
	}
	return os.WriteFile(filepath.Join(dir, "NOT-RESTORED.txt"), []byte(b.String()), 0o644)
}

// strip removes fields the API server rejects or ignores on apply. An object
// still carrying its old resourceVersion or uid cannot be created fresh,
// creationTimestamp is assigned by the server, and status is a separate
// subresource a plain apply cannot set anyway. Leaving any of these in
// produces a file that looks like a manifest but that kubectl apply -f will
// refuse or silently not restore -- not an undo plan.
func strip(raw []byte) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	if md, ok := obj["metadata"].(map[string]any); ok {
		delete(md, "resourceVersion")
		delete(md, "uid")
		delete(md, "creationTimestamp")
	}
	delete(obj, "status")
	return json.MarshalIndent(obj, "", "  ")
}

// fetch retrieves an object's full body. It goes through the discovery
// client's own REST client rather than a resource-typed one: objs carries
// whatever GVR the live cluster's discovery reported, which this tool never
// compiles against, so a typed client cannot reach most of it. Building the
// request path from the target and reading Raw() bytes back sidesteps
// decoding into any Go type sounding knows about -- the bytes the server
// hands back are exactly what a later apply needs to see, not this tool's
// understanding of the shape.
func fetch(ctx context.Context, c *cluster.Clients, t model.Target) ([]byte, error) {
	var p string
	if t.Group == "" {
		p = path.Join("/api", t.Version, "namespaces", t.Namespace, t.Resource, t.Name)
	} else {
		p = path.Join("/apis", t.Group, t.Version, "namespaces", t.Namespace, t.Resource, t.Name)
	}
	raw, err := c.Discovery.RESTClient().Get().AbsPath(p).Do(ctx).Raw()
	*c.Calls++
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// Write fetches the full body of every object and writes it to dir, plus a
// restore ordering. It is the undo plan.
func Write(ctx context.Context, c *cluster.Clients, dir string, objs []cascade.Object, excluded []string) (*model.UndoPlan, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating snapshot dir: %w", err)
	}

	ordered := cascade.Order(objs)
	files := make([]string, 0, len(ordered))
	for _, o := range ordered {
		body, err := fetch(ctx, c, o.Target)
		if err != nil {
			return nil, fmt.Errorf("fetching %s %s/%s: %w", o.Target.Resource, o.Target.Namespace, o.Target.Name, err)
		}
		stripped, err := strip(body)
		if err != nil {
			return nil, fmt.Errorf("stripping %s %s/%s: %w", o.Target.Resource, o.Target.Namespace, o.Target.Name, err)
		}
		name := safeFilename(o.Target.Resource, o.Target.Name)
		if err := os.WriteFile(filepath.Join(dir, name), stripped, 0o644); err != nil {
			return nil, fmt.Errorf("writing %s: %w", name, err)
		}
		files = append(files, name)
	}

	if err := writeOrdering(dir, files); err != nil {
		return nil, fmt.Errorf("writing restore ordering: %w", err)
	}
	if err := writeExclusions(dir, excluded); err != nil {
		return nil, fmt.Errorf("writing exclusions: %w", err)
	}

	return &model.UndoPlan{
		Dir:      dir,
		Objects:  len(files),
		Excluded: excluded,
	}, nil
}
