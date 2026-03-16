# Running Kepler E2E Tests on OpenShift

Step-by-step guide for deploying Kepler and running the k8s e2e test suite
on an existing OpenShift cluster.

## Prerequisites

- OpenShift cluster with `kube:admin` access
- `oc` / `kubectl` CLI logged in
- `podman` or `docker` for image builds
- Go 1.24+ installed locally
- quay.io account (repo must be public or have pull secret configured)

## Cluster Info (current)

- **Cluster**: ikanse-135.qe.devcluster.openshift.com
- **Version**: OpenShift 4.20.10
- **Nodes**: 3 control-plane + 3 workers (x86_64, RHEL CoreOS)

---

## Step 1: Build the Kepler image

```bash
cd ~/src/powermon/kepler

# Set your image name
export KEPLER_IMAGE=quay.io/vimalkum/kepler:e2e-test

make image KEPLER_IMAGE=$KEPLER_IMAGE
```

This uses the multi-stage Dockerfile (golang:1.24 builder → UBI9 runtime).

## Step 2: Push to quay.io

```bash
# Login if needed
podman login quay.io

make push KEPLER_IMAGE=$KEPLER_IMAGE
```

Make sure the quay.io repo (`vimalkum/kepler`) is **public**, or create an
image pull secret in the `kepler` namespace.

## Step 3: Deploy Kepler

```bash
make deploy KEPLER_IMAGE=$KEPLER_IMAGE
```

This applies kustomize manifests from `manifests/k8s/`:
namespace, ServiceAccount, ClusterRole/Binding, ConfigMap, DaemonSet,
Service, ServiceMonitor.

## Step 4: Grant privileged SCC (OpenShift-specific)

The Kepler DaemonSet requires `privileged: true` and `hostPID: true`.
OpenShift blocks this unless the ServiceAccount has the `privileged` SCC:

```bash
oc adm policy add-scc-to-user privileged -z kepler -n kepler

# Restart pods so they pick up the SCC
oc delete pods -n kepler --all
```

## Step 5: Verify deployment

```bash
# Watch pods come up (expect 6 — one per node)
kubectl get pods -n kepler -w

# Check DaemonSet status
kubectl get ds -n kepler

# Quick smoke test — check metrics endpoint
kubectl port-forward -n kepler ds/kepler 28282:28282 &
curl -s http://localhost:28282/metrics | head -20
kill %1
```

## Step 6: Run K8s E2E tests

```bash
make test-e2e-k8s
```

This runs: `cd test/e2e-k8s && go test -v -timeout=15m .`

The test suite will:

1. Verify the Kepler DaemonSet has ready pods
2. Create a `kepler-e2e-test` namespace
3. Port-forward to a Kepler pod (28282 → 28284)
4. Run tests: pod/container metrics, hierarchy, workload detection
   (deploys stress-ng DaemonSet), power attribution, terminated tracking
5. Clean up the test namespace

**Optional flags** (pass via `-args`):

```bash
cd test/e2e-k8s && go test -v -timeout=15m . \
  -kepler.namespace=kepler \
  -kepler.service=kepler \
  -kepler.metrics-port=28282 \
  -kepler.local-port=28284
```

## Step 7: Cleanup

```bash
make undeploy
```

---

## Troubleshooting

### Pods stuck in `CreateContainerError` or `CrashLoopBackOff`

Most likely missing SCC. Verify:

```bash
oc get pods -n kepler -o yaml | grep -A5 "message:"
oc adm policy who-can use scc privileged -n kepler
```

### Power metrics are zero

AWS instances don't expose Intel RAPL. Kepler will run but report zero
power. To get non-zero values for testing, enable the fake CPU meter in
the ConfigMap (`manifests/k8s/configmap.yaml`):

```yaml
dev:
  fake-cpu-meter:
    enabled: true
```

Then redeploy: `make deploy KEPLER_IMAGE=$KEPLER_IMAGE`

### stress-ng image pull fails

Tests deploy `polinux/stress-ng:latest` from Docker Hub. If rate-limited:

```bash
# Pre-pull and mirror to internal registry
oc import-image stress-ng --from=docker.io/polinux/stress-ng:latest \
  --confirm -n kepler-e2e-test
```

### Port-forward fails

If local port 28284 is in use, kill existing port-forwards:

```bash
lsof -i :28284 | awk 'NR>1{print $2}' | xargs kill
```
