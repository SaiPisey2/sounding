# sounding

`sounding score '<command>'` reports what a Kubernetes mutation would
destroy, computed from live cluster state. It executes nothing.

```
sounding score 'delete ns checkout' [--snapshot DIR] [--kubeconfig PATH] [--json] [--all]
sounding score --stdin [--snapshot DIR] [--kubeconfig PATH] [--json] [--all]
```

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

## What it does not do

- It executes nothing. It never issues a write, a delete, or a patch against
  the cluster it scores.
- It issues no write, delete or patch against a cluster (see
  `internal/cluster/readonly_test.go`, which checks this mechanically rather
  than by review). It needs only `list` and `get` to do its job, and the
  credential you run it with should be scoped to exactly those two verbs --
  `cluster.New` builds clients straight from whatever kubeconfig it is
  given, with no scoping or impersonation of its own, so a credential wider
  than `list`/`get` (a cluster-admin context, for instance) could still
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
