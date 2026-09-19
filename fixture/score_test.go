//go:build integration

package fixture

import (
	"os/exec"
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

func TestOwnerChainIsReportedOwnerFirst(t *testing.T) {
	out, _ := score(t, "delete ns sounding-demo")
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

func classOf(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "class") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
