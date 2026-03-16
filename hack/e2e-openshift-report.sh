#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2025 The Kepler Authors
# SPDX-License-Identifier: Apache-2.0
#
# Run Kepler k8s e2e tests on OpenShift and generate a markdown report.
#
# Usage:
#   ./hack/e2e-openshift-report.sh [options]
#
# Options:
#   -n, --namespace NS    Kepler namespace (default: auto-detect)
#   -o, --output FILE     Output report file (default: kepler-e2e-report-<timestamp>.md)
#   -t, --timeout DUR     Test timeout (default: 15m)
#   --dry-run             Collect cluster info only, skip tests
#   -h, --help            Show this help

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Defaults
KEPLER_NS=""
TIMEOUT="15m"
DRY_RUN=false
TIMESTAMP="$(date -u +%Y%m%d-%H%M%S)"
OUTPUT=""
NS_EXPLICIT=false

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BOLD='\033[1m'
NC='\033[0m'

info() { echo -e "${BOLD}[INFO]${NC}  $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }

usage() {
	sed -n '/^# Usage:/,/^$/p' "$0" | sed 's/^# \?//'
	exit 0
}

while [[ $# -gt 0 ]]; do
	case $1 in
	-n | --namespace)
		KEPLER_NS="$2"
		NS_EXPLICIT=true
		shift 2
		;;
	-o | --output)
		OUTPUT="$2"
		shift 2
		;;
	-t | --timeout)
		TIMEOUT="$2"
		shift 2
		;;
	--dry-run)
		DRY_RUN=true
		shift
		;;
	-h | --help) usage ;;
	*)
		error "Unknown option: $1"
		usage
		;;
	esac
done

OUTPUT="${OUTPUT:-kepler-e2e-report-${TIMESTAMP}.md}"

# Prefer oc, fall back to kubectl
KUBE_CMD="kubectl"
if command -v oc &>/dev/null; then
	KUBE_CMD="oc"
fi

for cmd in "$KUBE_CMD" jq; do
	if ! command -v "$cmd" &>/dev/null; then
		error "$cmd is required but not found"
		exit 1
	fi
done

if ! "$KUBE_CMD" cluster-info &>/dev/null; then
	error "Cannot connect to cluster. Check your kubeconfig."
	exit 1
fi

info "Using $KUBE_CMD to interact with cluster"

# ── Collect cluster info ────────────────────────────────────────────

info "Collecting cluster info..."

API_URL=$("$KUBE_CMD" cluster-info 2>/dev/null | head -1 | grep -oP 'https://\S+' || echo "unknown")

OCP_VERSION="N/A"
if "$KUBE_CMD" get clusterversion version &>/dev/null; then
	OCP_VERSION=$("$KUBE_CMD" get clusterversion version -o jsonpath='{.status.desired.version}' 2>/dev/null || echo "N/A")
fi

NODES_JSON=$("$KUBE_CMD" get nodes -o json 2>/dev/null)

# ── Detect Kepler deployment ────────────────────────────────────────

KEPLER_LABEL=""
KEPLER_DS_NAME=""

detect_kepler() {
	local ns=$1 label=$2
	local count
	count=$("$KUBE_CMD" get pods -n "$ns" -l "$label" -o jsonpath='{.items}' 2>/dev/null | jq 'length' 2>/dev/null || echo 0)
	if [[ "$count" -gt 0 ]]; then
		KEPLER_NS="$ns"
		KEPLER_LABEL="$label"
		KEPLER_DS_NAME=$("$KUBE_CMD" get daemonsets -n "$ns" \
			-o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
		return 0
	fi
	return 1
}

if [[ "$NS_EXPLICIT" == true ]]; then
	if detect_kepler "$KEPLER_NS" "app.kubernetes.io/name=kepler"; then
		:
	elif detect_kepler "$KEPLER_NS" "app.kubernetes.io/name=power-monitor-exporter"; then
		:
	fi
else
	if detect_kepler "kepler" "app.kubernetes.io/name=kepler"; then
		:
	elif detect_kepler "power-monitor" "app.kubernetes.io/name=power-monitor-exporter"; then
		:
	elif detect_kepler "openshift-power-monitoring" "app.kubernetes.io/name=power-monitor-exporter"; then
		:
	fi
fi

if [[ -z "$KEPLER_LABEL" ]]; then
	error "No Kepler pods found. Searched namespaces: kepler, power-monitor, openshift-power-monitoring"
	exit 1
fi

info "Found Kepler in namespace: ${KEPLER_NS} (label: ${KEPLER_LABEL})"

KEPLER_PODS_JSON=$("$KUBE_CMD" get pods -n "${KEPLER_NS}" -l "${KEPLER_LABEL}" -o json 2>/dev/null || echo '{"items":[]}')

KEPLER_DS_IMAGE="unknown"
if [[ -n "$KEPLER_DS_NAME" ]]; then
	KEPLER_DS_IMAGE=$("$KUBE_CMD" get daemonset "$KEPLER_DS_NAME" -n "${KEPLER_NS}" -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || echo "unknown")
else
	KEPLER_DS_IMAGE=$(echo "$KEPLER_PODS_JSON" | jq -r '.items[0].spec.containers[0].image // "unknown"')
fi

KEPLER_POD=$(echo "$KEPLER_PODS_JSON" | jq -r '.items[0].metadata.name // empty')
KEPLER_VERSION="unknown"
if [[ -n "$KEPLER_POD" ]]; then
	KEPLER_VERSION=$({ "$KUBE_CMD" logs -n "${KEPLER_NS}" "$KEPLER_POD" 2>/dev/null |
		grep -oP -m1 ' version=\K\S+' || true; } | head -1)
	KEPLER_VERSION="${KEPLER_VERSION:-unknown}"
fi

# ── Run tests ───────────────────────────────────────────────────────

TEST_JSON_FILE=$(mktemp)
TEST_EXIT_CODE=0

if [[ "$DRY_RUN" == true ]]; then
	info "Dry run: skipping tests"
	echo '{}' >"$TEST_JSON_FILE"
else
	info "Running e2e-k8s tests (timeout: ${TIMEOUT})..."

	cd "${PROJECT_ROOT}/test/e2e-k8s"

	set +e
	go test -v -json -timeout="${TIMEOUT}" -count=1 \
		-kepler.namespace="${KEPLER_NS}" \
		-kepler.daemonset="${KEPLER_DS_NAME}" \
		-kepler.pod-label="${KEPLER_LABEL}" \
		. 2>&1 | tee "$TEST_JSON_FILE"
	TEST_EXIT_CODE=$?
	set -e

	cd "${PROJECT_ROOT}"
fi

# ── Parse test results ──────────────────────────────────────────────

info "Generating report..."

RESULTS_JSON=$(jq -s '
    [ .[] | select(.Test != null and (.Action == "pass" or .Action == "fail" or .Action == "skip")) ]
    | group_by(.Test)
    | map(last)
' "$TEST_JSON_FILE" 2>/dev/null || echo '[]')

TOTAL=$(echo "$RESULTS_JSON" | jq 'length')
PASS=$(echo "$RESULTS_JSON" | jq '[ .[] | select(.Action == "pass") ] | length')
FAIL=$(echo "$RESULTS_JSON" | jq '[ .[] | select(.Action == "fail") ] | length')
SKIP=$(echo "$RESULTS_JSON" | jq '[ .[] | select(.Action == "skip") ] | length')

# ── Generate report ─────────────────────────────────────────────────

{
	echo "# Kepler E2E Test Report"
	echo ""
	echo "- **Date**: $(date -u '+%Y-%m-%d %H:%M:%S UTC')"
	echo "- **Cluster**: ${API_URL}"
	echo "- **OCP Version**: ${OCP_VERSION}"
	echo "- **Kepler Version**: ${KEPLER_VERSION}"
	echo "- **Kepler Image**: \`${KEPLER_DS_IMAGE}\`"
	echo ""

	echo "## Nodes"
	echo ""
	echo "| Name | Role | OS Image | Kernel | Arch |"
	echo "|------|------|----------|--------|------|"
	echo "$NODES_JSON" | jq -r '
        .items[] |
        .metadata.name as $name |
        ([ .metadata.labels | to_entries[] |
            select(.key | startswith("node-role.kubernetes.io/")) |
            .key | ltrimstr("node-role.kubernetes.io/")
        ] | join(",")) as $roles |
        .status.nodeInfo.osImage as $os |
        .status.nodeInfo.kernelVersion as $kernel |
        .status.nodeInfo.architecture as $arch |
        "| \($name) | \($roles) | \($os) | \($kernel) | \($arch) |"
    '
	echo ""

	echo "## Kepler Deployment"
	echo ""
	echo "| Pod | Node | Status | Image | Restarts |"
	echo "|-----|------|--------|-------|----------|"
	echo "$KEPLER_PODS_JSON" | jq -r '
        .items[] |
        .metadata.name as $pod |
        .spec.nodeName as $node |
        .status.phase as $status |
        .spec.containers[0].image as $image |
        ([.status.containerStatuses[]?.restartCount] | add // 0) as $restarts |
        "| \($pod) | \($node) | \($status) | \($image) | \($restarts) |"
    '
	echo ""

	if [[ "$DRY_RUN" == false ]]; then
		echo "## Test Results"
		echo ""

		if [[ "$TOTAL" -gt 0 ]]; then
			echo "| Test | Status | Duration |"
			echo "|------|--------|----------|"

			echo "$RESULTS_JSON" | jq -r '
                .[] |
                .Test as $test |
                (.Action | if . == "pass" then "PASS"
                           elif . == "fail" then "**FAIL**"
                           else "SKIP" end) as $status |
                (if .Elapsed then (.Elapsed | tostring | .[0:6]) + "s" else "-" end) as $dur |
                "| \($test) | \($status) | \($dur) |"
            '
		else
			echo "*No test results captured.*"
		fi
		echo ""

		echo "## Summary"
		echo ""
		echo "| Total | Pass | Fail | Skip |"
		echo "|-------|------|------|------|"
		echo "| ${TOTAL} | ${PASS} | ${FAIL} | ${SKIP} |"
		echo ""

		if [[ "$FAIL" -gt 0 ]]; then
			echo "**Result: FAIL**"
		elif [[ "$TOTAL" -eq 0 ]]; then
			echo "**Result: NO TESTS RAN**"
		else
			echo "**Result: PASS**"
		fi
	else
		echo "*Tests skipped (dry-run mode).*"
	fi

} >"$OUTPUT"

rm -f "$TEST_JSON_FILE"

echo ""
info "Report written to: ${OUTPUT}"

if [[ "$DRY_RUN" == false ]]; then
	if [[ "$FAIL" -gt 0 ]]; then
		echo -e "${RED}${BOLD}FAIL${NC} — ${TOTAL} tests: ${PASS} passed, ${FAIL} failed, ${SKIP} skipped"
	elif [[ "$TOTAL" -eq 0 ]]; then
		warn "No test results captured"
	else
		echo -e "${GREEN}${BOLD}PASS${NC} — ${TOTAL} tests: ${PASS} passed, ${SKIP} skipped"
	fi
fi

exit "$TEST_EXIT_CODE"
