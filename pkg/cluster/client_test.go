package cluster

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/rest"
)

// clientcmd.BuildConfigFromFlags("", "") ignores $KUBECONFIG entirely and
// goes straight to the in-cluster loader -- outside a pod that fails with
// advice about --master and KUBERNETES_MASTER, flags this CLI does not
// have. New's default path must actually honour $KUBECONFIG, which is the
// tool's single most common invocation: no flag, an ordinary kubeconfig at
// the default location.
func TestNewUsesKUBECONFIGWhenNoPathIsGiven(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	kubeconfig := `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://example.invalid:6443
  name: test
contexts:
- context:
    cluster: test
    user: test
  name: test
current-context: test
users:
- name: test
  user: {}
`
	if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
		t.Fatalf("writing fake kubeconfig: %v", err)
	}
	t.Setenv("KUBECONFIG", path)

	c, err := New("")
	if err != nil {
		t.Fatalf("New(\"\") did not honour $KUBECONFIG: %v", err)
	}
	if c.Discovery == nil || c.Dynamic == nil || c.Metadata == nil || c.Typed == nil {
		t.Error("New returned a Clients with a nil client")
	}
}

// With $KUBECONFIG pointing at a file that does not exist and no in-cluster
// environment, there is genuinely nothing to connect with, and New must
// still refuse to hand back a usable-looking client. Client-go's own
// generic "no configuration has been provided" message (which does mention
// KUBERNETES_MASTER, a real if obscure client-go fallback) is legitimate
// here, unlike the case a REAL kubeconfig is silently ignored --
// TestNewUsesKUBECONFIGWhenNoPathIsGiven above pins that one; this only
// confirms the true-emptiness case still fails rather than silently
// succeeding.
//
// This deliberately points $KUBECONFIG at a missing file rather than
// unsetting it: unsetting it falls back to client-go's own
// RecommendedHomeFile, which is a package-level variable computed from
// $HOME at process start, before any test can override it -- so on a
// machine that genuinely has a kubeconfig at ~/.kube/config, unsetting
// $KUBECONFIG in a test still reaches that real file and a real cluster's
// (possibly slow) authentication path. An explicit, missing $KUBECONFIG
// path is the deterministic way to reach "no config found" from a test.
func TestNewWithNoConfigAnywhereFailsRatherThanLookingUsable(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "does-not-exist"))
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	if _, err := New(""); err == nil {
		t.Fatal("want an error with no config anywhere, got nil")
	}
}

// A conformant cluster sends a Warning header on a deprecated API this tool
// legitimately listed -- v1 Endpoints, for one -- and the default client-go
// handler prints that above the report on every single run. It describes
// the cluster, not a mistake this tool made, and it is not this tool's
// place to relay it.
func TestSuppressServerWarningsDisablesTheDefaultHandler(t *testing.T) {
	cfg := &rest.Config{}
	suppressServerWarnings(cfg)
	if _, ok := cfg.WarningHandler.(rest.NoWarnings); !ok {
		t.Errorf("WarningHandler = %#v, want rest.NoWarnings{}", cfg.WarningHandler)
	}
}

// blastgate hands sounding a config it keeps using for its own forwarding.
// suppressServerWarnings writes to the config it is given; applied to the
// caller's copy it would silently change how the caller's own clients log.
func TestNewForConfigDoesNotMutateTheCallersConfig(t *testing.T) {
	cfg := &rest.Config{Host: "https://127.0.0.1:1"}
	c, err := NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WarningHandler != nil {
		t.Error("NewForConfig set WarningHandler on the caller's config")
	}
	if cfg.WrapTransport != nil {
		t.Error("NewForConfig set WrapTransport on the caller's config")
	}
	if c.Calls == nil || c.Typed == nil || c.Dynamic == nil || c.Metadata == nil || c.Discovery == nil {
		t.Fatalf("incomplete clients: %+v", c)
	}
}

// The discovery transport is the only place discovery requests are counted
// (see New's comment). Moving construction into NewForConfig must keep it.
func TestNewForConfigStillCountsDiscoveryRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"major":"1","minor":"37","gitVersion":"v1.37.0"}`))
	}))
	defer srv.Close()

	c, err := NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Discovery.ServerVersion(); err != nil {
		t.Fatal(err)
	}
	if *c.Calls != 1 {
		t.Errorf("calls = %d, want 1", *c.Calls)
	}
}
