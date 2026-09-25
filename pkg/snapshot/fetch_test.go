package snapshot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/SaiPisey2/sounding/pkg/cluster"
	"github.com/SaiPisey2/sounding/pkg/model"
)

// A hand-built request path can be *cleaned* by path.Join rather than
// rejected: "../../secrets/admin" resolves to a different, real API path
// instead of erroring, and whatever it fetches gets written into the bundle
// under the name that was actually asked for. This proves fetch cannot be
// made to do that -- not by inspecting a string it builds (it no longer
// builds one), but by recording whether a request for a crafted name ever
// reaches the server at all, and, as a control, that an ordinary name still
// does.
func TestFetchCannotEscapeToADifferentObject(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"prod"}}`))
	}))
	defer srv.Close()

	dyn, err := dynamic.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatalf("building dynamic client against fake server: %v", err)
	}
	var calls int64
	c := &cluster.Clients{Dynamic: dyn, Calls: &calls}

	for _, name := range []string{"../../secrets/admin", "..", "a/b"} {
		gotPath = ""
		target := model.Target{Version: "v1", Resource: "pods", Namespace: "prod", Name: name}
		if _, err := fetch(context.Background(), c, target); err == nil {
			t.Errorf("fetch(%q) succeeded, want it refused before any request could resolve to a different object", name)
		}
		if gotPath != "" {
			t.Errorf("fetch(%q) reached the server at %q -- a crafted name must never produce a request", name, gotPath)
		}
	}

	// Control: an ordinary name must still work, so the assertions above are
	// pinning a refusal, not an inert dynamic client that never calls out.
	gotPath = ""
	target := model.Target{Version: "v1", Resource: "pods", Namespace: "prod", Name: "web-1"}
	if _, err := fetch(context.Background(), c, target); err != nil {
		t.Fatalf("fetch of an ordinary name failed: %v", err)
	}
	if gotPath == "" {
		t.Fatal("fetch of an ordinary name never reached the server")
	}
}
