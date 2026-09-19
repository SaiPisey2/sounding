//go:build integration

package fixture

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// fixtureKubeconfigSuffix and fixtureContext name the two things that can
// prove a run is aimed at this fixture's own disposable cluster rather than
// whatever cluster a workstation's default kubeconfig happens to point at.
const (
	fixtureKubeconfigSuffix = "fixture/kubeconfig"
	fixtureContext          = "kind-sounding-fixture"
)

// TestMain is the guard `make demo-test` doesn't need but a bare `go test
// -tags=integration ./fixture` -- or `-run TestOne` typed from inside this
// directory while debugging a single failure, which is what actually
// happens in practice -- absolutely does. score() execs ../sounding with
// the process's inherited environment and never passes --kubeconfig; only
// the Makefile's own `KUBECONFIG=...` prefix keeps that exec off whatever
// cluster the environment's default context resolves to. On a workstation
// whose ~/.kube/config points at a real, populated cluster, running this
// package's tests any other way would run a full discovery sweep plus one
// List per namespaced resource against production, seeking a namespace
// that was never seeded there.
//
// Refusing here, before any test body runs, means the check happens once,
// cannot be forgotten by a future test added to this package, and -- since
// it happens before anything execs the binary -- never itself touches a
// cluster it hasn't first confirmed is the right one.
func TestMain(m *testing.M) {
	if err := requireFixtureCluster(); err != nil {
		fmt.Fprintf(os.Stderr, "refusing to run: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// requireFixtureCluster checks the same two things the brief allows: an
// explicit KUBECONFIG naming this fixture's own file, or -- when
// KUBECONFIG is unset, the same case sounding itself falls back to the
// environment's default resolution for -- the default context already
// being the fixture's. The second branch shells out to `kubectl config
// current-context`, which reads a local file and contacts no cluster, so a
// refusal here never costs an API call against the wrong one.
func requireFixtureCluster() error {
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		for _, p := range strings.Split(kc, string(os.PathListSeparator)) {
			if strings.HasSuffix(p, fixtureKubeconfigSuffix) {
				return nil
			}
		}
		return fmt.Errorf("KUBECONFIG=%q does not end in %q -- this suite only runs against the fixture's own cluster (see the Makefile's demo-test target)", kc, fixtureKubeconfigSuffix)
	}

	out, err := exec.Command("kubectl", "config", "current-context").CombinedOutput()
	if err != nil {
		return fmt.Errorf("KUBECONFIG is unset and the default context could not be read: %v", err)
	}
	ctx := strings.TrimSpace(string(out))
	if ctx != fixtureContext {
		return fmt.Errorf("KUBECONFIG is unset and the default context is %q, not %q -- this suite only runs against the fixture's own cluster (see the Makefile's demo-test target)", ctx, fixtureContext)
	}
	return nil
}
