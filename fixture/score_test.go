//go:build integration

package fixture

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func score(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("../sounding", append([]string{"score"}, args...)...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running sounding: %v", err)
	}
	return string(out), code
}

// The core claim, against a live cluster: the same namespace grades
// differently depending on one field of one PersistentVolume.
func TestReclaimPolicyFlipsTheClassAgainstALiveCluster(t *testing.T) {
	out, code := score(t, "delete ns sounding-demo")
	if !strings.Contains(out, "TERMINAL") {
		t.Errorf("want TERMINAL while a Delete-policy pv is present:\n%s", out)
	}
	if code != 4 {
		t.Errorf("exit = %d, want 4", code)
	}
	if !strings.Contains(out, "pv-delete") {
		t.Errorf("report must name the volume whose data is lost:\n%s", out)
	}
}

func TestRetainOnlyNamespaceIsCompensable(t *testing.T) {
	out, code := score(t, "delete ns sounding-retain-only")
	if !strings.Contains(out, "COMPENSABLE") {
		t.Errorf("want COMPENSABLE when every pv is Retain:\n%s", out)
	}
	if code != 3 {
		t.Errorf("exit = %d, want 3", code)
	}
	// Its sibling (TestReclaimPolicyFlipsTheClassAgainstALiveCluster) checks
	// that the volume actually responsible for a verdict is named. Without
	// this, COMPENSABLE and exit 3 are also exactly what "delete
	// namespace"'s own verb floor produces on their own, with the PVC->PV
	// join never consulted at all -- this test would keep passing even with
	// volume.Join disabled outright, catching an object over-reported but
	// never one silently dropped, which is the direction that loses data.
	if !strings.Contains(out, "pv-retain") {
		t.Errorf("report must name the volume examined, not just agree with the verb floor by coincidence:\n%s", out)
	}
}

// Discovery is live, so a CRD registered after the binary starts must still
// be enumerated. A cached discovery client would pass every other test here
// and fail this one.
func TestACRDRegisteredNowIsEnumerated(t *testing.T) {
	out, _ := score(t, "delete ns sounding-crd")
	if !strings.Contains(out, "Widget") {
		t.Errorf("a custom resource in the namespace was not enumerated:\n%s", out)
	}
}

// Ordering is a property of the complete listing, not of whatever the
// default cap happens to let through. sounding-demo's namespace generates
// enough core-group objects (Events chief among them, one per pod
// lifecycle step) that the default 20-effect cap fills before the
// Deployment/ReplicaSet chain -- which sorts after every core-group
// resource, since cluster.ListableNamespaced orders resources by
// GroupVersionResource string and "" (core) sorts before "apps" -- ever
// appears. Asserting against the capped view tests what the cap chose to
// show, not what cascade.Order actually produced; --all is the view where
// the ordering claim is real.
func TestOwnerChainIsReportedOwnerFirst(t *testing.T) {
	out, _ := score(t, "delete ns sounding-demo", "--all")
	d, r := strings.Index(out, "Deployment/"), strings.Index(out, "ReplicaSet/")
	p := strings.Index(out, "Pod/")
	if !(d >= 0 && r > d && p > r) {
		t.Errorf("want Deployment before ReplicaSet before Pod:\n%s", out)
	}
}

// The flag must not change the verdict. A flag that changes a class is
// invisible until the day it matters.
func TestSnapshotDoesNotChangeTheClass(t *testing.T) {
	plain, plainCode := score(t, "delete ns sounding-demo")
	snap, snapCode := score(t, "delete ns sounding-demo", "--snapshot", t.TempDir())
	if plainCode != snapCode {
		t.Errorf("exit codes differ with and without --snapshot: %d vs %d", plainCode, snapCode)
	}
	if classOf(plain) != classOf(snap) {
		t.Errorf("class differs with and without --snapshot:\n%s\n---\n%s", plain, snap)
	}
}

func TestAnUnanalysedVerbRefusesAndNamesIt(t *testing.T) {
	out, code := score(t, "scale deployment api --replicas=0 -n sounding-demo")
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(out, "scale") {
		t.Errorf("refusal must name the verb it could not analyse:\n%s", out)
	}
}

func TestTwoRunsAgree(t *testing.T) {
	a, ca := score(t, "delete ns sounding-demo")
	b, cb := score(t, "delete ns sounding-demo")
	if ca != cb || classOf(a) != classOf(b) {
		t.Errorf("two consecutive scans disagreed")
	}
}

// sounding-demo's fixture is known by construction, so a live cluster is
// the one place its object count has an exact right answer rather than a
// merely plausible-looking one. Events are the one exception: how many a
// real kubelet emits while getting two pods to Running -- scheduling,
// pulling an image that may or may not already be cached, starting each
// container -- is a property of cluster timing, not of the seed, and it
// measurably differs between two runs seconds apart on the same cluster (11
// vs. a different count observed while writing this). So this pins every
// OTHER kind's count exactly, built from named constants that mirror
// fixture/seed/02-sounding-demo.yaml plus the well-known objects
// Kubernetes and its own controllers add on top of it, and treats the
// Event count as a floor rather than an exact value: enumerating zero
// Events would still be wrong and this catches that, but enumerating a
// different positive number on the next run is not a regression.
const (
	seedDeployments     = 1 // api
	seedReplicaSets     = 1 // api's one ReplicaSet; nothing here ever triggers a rollout
	seedPods            = 2 // api's spec.replicas
	seedServices        = 1 // api
	seedEndpoints       = 1 // one per Service, from the endpoints controller
	seedEndpointSlices  = 1 // one per Service, from the endpointslice controller
	seedConfigMaps      = 2 // api-config (seeded) + kube-root-ca.crt (every namespace gets one)
	seedSecrets         = 1 // api-secret
	seedServiceAccounts = 1 // "default", created per namespace
	seedPVCs            = 2 // data-retain, data-delete

	// One volume.Join effect per PVC -- destroys-data or detaches-data,
	// naming the bound PersistentVolume -- counted separately from the
	// PVC's own "destroys" effect already counted in seedPVCs above.
	seedVolumeJoinEffects = 2

	seedMinEvents = 1
)

var seedNonEventEffects = seedDeployments + seedReplicaSets + seedPods + seedServices +
	seedEndpoints + seedEndpointSlices + seedConfigMaps + seedSecrets +
	seedServiceAccounts + seedPVCs + seedVolumeJoinEffects

// This is the assertion that would have caught the Event double-enumeration
// bug on day one: that bug inflated exactly this number (the report's own
// "objects N" count), while every other assertion in this file -- class,
// exit code, which volume is named, ordering -- stayed satisfied throughout,
// because none of them look at how many objects there are, only at which
// ones and in what order.
func TestObjectCountMatchesKnownConstruction(t *testing.T) {
	out, _ := score(t, "delete ns sounding-demo", "--all")
	total := objectsHeaderCount(t, out)
	events := strings.Count(out, "Event/")
	nonEvent := total - events

	if nonEvent != seedNonEventEffects {
		t.Errorf("non-Event effect count = %d, want %d (sounding-demo is known by construction; see the seed* constants):\n%s", nonEvent, seedNonEventEffects, out)
	}
	if events < seedMinEvents {
		t.Errorf("event count = %d, want at least %d -- Events must still be enumerated, just not pinned exactly:\n%s", events, seedMinEvents, out)
	}
}

// objectsHeaderCount parses the "objects N across K kinds" line report.Write
// always prints, rather than counting effect lines by eye: the header's own
// count is what a reader actually sees and is the value this test exists to
// pin, and parsing it this way also catches a report that silently drops
// the header line itself.
func objectsHeaderCount(t *testing.T, out string) int {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "objects" && i+1 < len(fields) {
				n, err := strconv.Atoi(fields[i+1])
				if err != nil {
					t.Fatalf("objects line has a non-numeric count %q:\n%s", fields[i+1], out)
				}
				return n
			}
		}
	}
	t.Fatalf("report has no \"objects N across K kinds\" line:\n%s", out)
	return 0
}

func classOf(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "class") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
