# CLUSTER is its own kind cluster, and KUBECONFIG_FILE is its own
# kubeconfig, written under fixture/ and gitignored. Every target below
# targets that file explicitly and never falls back to the environment's
# own KUBECONFIG or ~/.kube/config, because a workstation running this
# fixture may have a real, populated cluster set as its default context and
# nothing here may ever touch it.
CLUSTER        := sounding-fixture
KUBECONFIG_FILE := $(CURDIR)/fixture/kubeconfig
KC              := KUBECONFIG=$(KUBECONFIG_FILE) kubectl

.PHONY: build demo-up demo-down demo-test

# The integration tests exec ../sounding as a subprocess -- see
# fixture/score_test.go -- so the binary must exist at the repo root, built
# fresh, before demo-test runs. demo-test depends on this rather than
# assuming a stale binary from an earlier session is still correct.
build:
	CGO_ENABLED=0 go build -o sounding ./cmd/sounding

demo-up:
	@echo "==> cluster: $(CLUSTER) (context kind-$(CLUSTER), kubeconfig $(KUBECONFIG_FILE))"
	@if ! kind get clusters | grep -qx '$(CLUSTER)'; then \
		kind create cluster --name $(CLUSTER) --config fixture/kind.yaml --kubeconfig $(KUBECONFIG_FILE); \
	else \
		echo "kind cluster $(CLUSTER) already exists, reusing it"; \
		kind get kubeconfig --name $(CLUSTER) > $(KUBECONFIG_FILE); \
	fi
	$(KC) wait --for=condition=Ready node --all --timeout=120s
	@echo "==> asserting widgets.sounding.dev is not already registered"
	@if $(KC) get crd widgets.sounding.dev >/dev/null 2>&1; then \
		echo "widgets.sounding.dev is already registered -- demo-down did not clean up the last run" >&2; \
		exit 1; \
	fi
	@echo "==> seeding namespaces, volumes and workloads"
	$(KC) apply -f fixture/seed/00-namespaces.yaml
	$(KC) apply -f fixture/seed/01-persistentvolumes.yaml
	$(KC) apply -f fixture/seed/02-sounding-demo.yaml
	$(KC) apply -f fixture/seed/03-sounding-retain-only.yaml
	@echo "==> waiting for the pvcs to bind"
	$(KC) wait --for=jsonpath='{.status.phase}'=Bound pvc/data-retain pvc/data-delete -n sounding-demo --timeout=60s
	$(KC) wait --for=jsonpath='{.status.phase}'=Bound pvc/data -n sounding-retain-only --timeout=60s
	@echo "==> waiting for the deployment's pods to exist"
	$(KC) wait --for=jsonpath='{.status.replicas}'=2 deployment/api -n sounding-demo --timeout=60s
	@echo "==> registering the widget CRD -- after the cluster and seed already exist"
	$(KC) apply -f fixture/crd/widget-crd.yaml
	$(KC) wait --for=condition=Established crd/widgets.sounding.dev --timeout=60s
	$(KC) apply -f fixture/seed/04-sounding-crd-widget.yaml

# Deletes the CRD explicitly, before anything else: discovery is live, so a
# CRD left registered would change every later enumeration this binary ever
# runs against this cluster, not only Widget's own. Deleting the namespaces
# does not remove it -- a CRD is cluster-scoped, same as the PVs it sits
# beside.
demo-down:
	@echo "==> deleting the widget CRD explicitly"
	-$(KC) delete crd widgets.sounding.dev --ignore-not-found --timeout=60s
	@echo "==> deleting seeded namespaces"
	-$(KC) delete ns sounding-demo sounding-retain-only sounding-crd --ignore-not-found --timeout=120s
	@echo "==> deleting cluster-scoped persistent volumes"
	-$(KC) delete pv pv-retain pv-delete pv-retain-only --ignore-not-found --timeout=60s
	@echo "==> deleting the kind cluster"
	-kind delete cluster --name $(CLUSTER) --kubeconfig $(KUBECONFIG_FILE)
	rm -f $(KUBECONFIG_FILE)

# -count=1 forces both runs of `make demo-test && make demo-test` to
# actually execute against the cluster rather than let Go's test cache
# report a cached pass the second time, which would prove nothing about
# repeat-run stability.
demo-test: build
	cd fixture && KUBECONFIG=$(KUBECONFIG_FILE) go test -tags=integration -count=1 -v .
