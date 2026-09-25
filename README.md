# sounding

`sounding score '<command>'` reports what a Kubernetes mutation would
destroy, computed from live cluster state. It executes nothing.

```
sounding score 'delete ns checkout' [--snapshot DIR] [--kubeconfig PATH] [--json] [--all]
sounding score --stdin [--snapshot DIR] [--kubeconfig PATH] [--json] [--all]
```

`sounding -h`, `sounding --help` and `sounding help` all print the usage
block above plus the exit-code table below to stdout and exit 0; `sounding
score -h` prints the same table alongside the flags.

## What it does

Given a `kubectl`-shaped delete command, sounding lists every object in the
target namespace across every resource the cluster's own discovery reports,
follows each PersistentVolumeClaim to its bound PersistentVolume, and
classifies the result:

| class        | exit | meaning                                              |
|--------------|------|-------------------------------------------------------|
| READ         | 0    | nothing destructive was found                         |
| REVERSIBLE   | 0    | destructive, but trivially undone                      |
| COMPENSABLE  | 3    | destructive; every effect is restorable                |
| TERMINAL     | 4    | at least one effect's data cannot come back            |
| AUTHORITY    | 5    | the action itself changes who can act, not just what exists |

A malformed or unsupported command refuses with exit 2 (nothing was scored).
Any other failure -- a bad kubeconfig, a network error, a disk write
failure -- exits 1.

The one comparison that matters: the same namespace grades TERMINAL when a
bound PersistentVolume's `reclaimPolicy` is `Delete`, and COMPENSABLE when
it is `Retain`. Nothing else about the namespace has to change.

It also scores deleting one object: `sounding score "delete deployment api -n shop"`
walks what the garbage collector would take with it -- ReplicaSets, Pods, and any
claim the object owns -- following Kubernetes' rule that a dependent goes only
when every one of its owners does. Deleting a Pod that a ReplicaSet, StatefulSet,
DaemonSet or ReplicationController manages scores `REVERSIBLE`: the controller puts
it back. Any other object, under any other controller or none, scores `COMPENSABLE`
or worse. An object delete needs `-n`; sounding cannot see your kubeconfig's
default namespace and will not guess it.

## What it does not do

- It executes nothing and never issues a write, a delete, or a patch against
  the cluster it scores. An AST census of every non-test file confirms the
  only client-go method calls are `Get`, `List` and `ListableNamespaced`;
  `pkg/cluster/readonly_test.go` mechanically rejects a known set of
  mutating calls as a sanity check. It needs only `list` and `get` to do its
  job, and the credential you run it with should be scoped to exactly those
  two verbs -- `cluster.New` builds clients straight from whatever kubeconfig
  it is given, with no scoping or impersonation of its own, so a credential
  wider than `list`/`get` (a cluster-admin context, for instance) could still
  perform the very mutation being scored. Read-only code is not the same
  thing as a read-only credential; only the credential you choose makes it
  one.
- `--snapshot DIR` writes an undo bundle -- the real manifests of every
  object that would be destroyed, plus a restore ordering -- to a local
  directory. It restores *objects*. It does not, and cannot, restore the
  data inside a volume whose reclaim policy already destroyed it; see
  `NOT-RESTORED.txt` in the bundle for exactly what a restore will not bring
  back.

## Worked example

```
$ sounding score 'delete ns checkout'

delete namespaces/checkout

  class     TERMINAL
  basis     computed (41/41 effects)
  objects   41 across 12 kinds
  scanned   2026-09-19T21:26:59Z, 39 api calls

  Nothing was executed. sounding issues no write, delete or patch against a cluster.

  destroys        ConfigMap/checkout-config          in the namespace
  ...
  destroys-data   PersistentVolume/pv-orders         pvc/orders-data is bound to pv/pv-orders with
                                                      reclaimPolicy=Delete: the csi driver destroys the
                                                      underlying volume and the data is not recoverable
  ... and 20 more (use --all to list every effect)
```

## Machine-readable I/O

`--json` and `--stdin` are v1.0.0's wire format, frozen as of this release:
every field below is a stable contract, not an implementation detail that
happens to be visible.

### `--stdin`: the Action schema

`sounding score --stdin` reads one JSON object from stdin, capped at 1 MiB,
in place of a command string:

```json
{
  "verb": "delete",
  "target": {
    "group": "",
    "version": "",
    "resource": "namespaces",
    "kind": "",
    "namespace": "",
    "name": "checkout"
  }
}
```

`verb` is required; everything else in `target` may be omitted except
`resource` and `name` for the one verb/resource pair this build actually
analyses (`delete` against `namespaces`). A body over 1 MiB, or one with no
`verb` at all, is refused rather than scored.

### `--json`: the Finding schema

`--json` encodes the full `Finding` this run produced, uncapped regardless
of `--all` -- a machine reader does not scroll past the safety line the way
a terminal does:

```json
{
  "action": { "verb": "delete", "target": { "resource": "namespaces", "name": "checkout" } },
  "effects": [
    {
      "kind": "destroys-data",
      "object": { "group": "", "version": "v1", "resource": "persistentvolumes", "kind": "PersistentVolume", "name": "pv-orders" },
      "basis": "computed",
      "explanation": "pvc/orders-data is bound to pv/pv-orders with reclaimPolicy=Delete: the csi driver destroys the underlying volume and the data is not recoverable"
    }
  ],
  "class": "TERMINAL",
  "undo": { "dir": "/tmp/undo", "objects": 40, "excluded": ["PersistentVolume/pv-orders: ..."] },
  "scanned": "2026-09-19T21:26:59Z",
  "apiCalls": 39
}
```

`class` is always the class name (`"TERMINAL"`, ...), never the underlying
integer -- `model.Class`'s int values collide with the exit codes of
*other* classes, so encoding one directly would print `3` for a TERMINAL
finding, which is COMPENSABLE's own exit code. `undo` is present only when
`--snapshot` ran.

## Limits

- **Time-of-check / time-of-use.** The `scanned` timestamp marks when
  enumeration started, not when a reader acts on the report. A cluster can
  change in between; nothing here watches for that.
- **Cost on large clusters.** A namespace deletion is scored by listing
  every listable namespaced resource cluster-wide, once each, then walking
  the objects that land in the target namespace. A cluster with many
  installed CRDs and controllers pays for every one of those `list` calls
  even when the target namespace is small. The report's `api calls` line
  states the real cost of the run it belongs to, rather than leaving it to
  be guessed.
- **The snapshot restores objects, not data.** `--snapshot` captures the
  manifests sounding can see through the Kubernetes API. It cannot capture
  what is inside a PersistentVolume, so a `Delete`-policy volume's data is
  gone regardless of whether `--snapshot` was used.
- **A REVERSIBLE Pod delete's snapshot still contains the Pod.** Its
  controller will already have created a replacement, so restoring the
  bundle's copy alongside it would create a duplicate.
- **REVERSIBLE does not check the replica count.** A Pod whose controller is
  scaled to zero, or a StatefulSet Pod above the ordinal a scale-down is
  about to remove, still scores `REVERSIBLE`, though nothing will recreate
  it. sounding reads objects through the metadata client, which does not
  return `spec.replicas`.

## The fixture

`fixture/` is an integration suite (`//go:build integration`, so `go test
./...` never touches a cluster) that runs the built binary against a real,
disposable `kind` cluster whose contents are known by construction: three
namespaces seeded with a Deployment→ReplicaSet→Pod chain, a Service, a
ConfigMap, a Secret, two PersistentVolumeClaims bound to `hostPath`
PersistentVolumes with different reclaim policies, one PersistentVolumeClaim
left deliberately unbound, and a CRD registered *after* the cluster is
already running.

```
make build       # builds ./sounding
make demo-up      # creates the kind cluster (context kind-sounding-fixture)
                    and seeds it -- never touches the default kubeconfig
make demo-test    # runs the integration suite against it
make demo-down    # deletes the CRD, the namespaces, the PVs, and the cluster
```

`demo-test` always rebuilds the binary first, and always runs with
`-count=1`, so two consecutive runs are two real executions against the
cluster, not one cached result reported twice.

## Using it as a library

The engine lives in `pkg/` and is importable:

| Package | What it does |
| --- | --- |
| `pkg/score` | scores an action end to end: `score.Score(ctx, clients, action, opts)` |
| `pkg/cluster` | builds the read-only clients (`New` from a kubeconfig, `NewForConfig` from a `rest.Config`) and resolves resources through discovery |
| `pkg/cascade` | walks ownerReferences; `Descendants` follows the garbage collector's every-owner-gone rule |
| `pkg/volume` | joins PVCs to PVs and decides whether data is destroyed |
| `pkg/disruption` | ready backends each Service keeps, and which PodDisruptionBudgets a removal breaks |
| `pkg/selector` | Services a label change disconnects; pods a selector change retargets |
| `pkg/model` | the effect and reversibility types every package shares |
| `pkg/snapshot` | captures manifests and writes a restore bundle |

`disruption` and `selector` are library-only in this release; the CLI does
not score `scale` or `patch` yet. `disruption.Assess(ctx, clients, ns, removal)`
takes either exact pod names or a `*metav1.LabelSelector` (a Deployment's
`spec.selector`, as it is) plus a count, and returns an error rather than an
empty report when the count is negative or has no selector.

A `cluster.Clients` is not safe for concurrent scoring: build one per
goroutine with `NewForConfig`. `Finding.APICalls` counts the requests of the
`Score` call that returned it. Setting `Target.Group` restricts which API
group the resource name is resolved in.

`internal/action` (command parsing) and `internal/report` (CLI output) stay
private. The read-only guard walks the whole module, so `pkg/` is held to
`Get` and `List` like everything else. The API follows semver from v1.1.0.

## License

Apache 2.0
