#!/usr/bin/env bash
# e2e-test-ocp.sh

set -o errexit
set -o nounset
set -o pipefail

export OC=$(which oc) # OpenShift CLI
SOURCE_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$SOURCE_DIR/.."
DEFAULT_NAMESPACE="kueue-system"
# This is required to reuse the exisiting code.
# Set this to empty value for OCP tests.
export E2E_KIND_VERSION=""
# shellcheck source=hack/e2e-common-ocp.sh
source "${SOURCE_DIR}/e2e-common.sh"

# To toggle deployment of cert-manager and kueue,
# set SKIP_DEPLOY to "true" to skip these steps.
SKIP_DEPLOY=${SKIP_DEPLOY:-false}

# To label worker nodes for e2e tests.
function label_worker_nodes() {
    echo "Labeling two worker nodes for e2e tests..."
    # Retrieve the names of nodes with the "worker" role.
    local nodes=($($OC get nodes -l node-role.kubernetes.io/worker -o jsonpath='{.items[*].metadata.name}'))
    
    if [ ${#nodes[@]} -lt 2 ]; then
        echo "Error: Found less than 2 worker nodes. Cannot assign labels."
        exit 1
    fi
    
    # Label the first node as "on-demand"
    $OC label node "${nodes[0]}" instance-type=on-demand --overwrite
    # Label the second node as "spot"
    $OC label node "${nodes[1]}" instance-type=spot --overwrite
    echo "Labeled ${nodes[0]} as on-demand and ${nodes[1]} as spot."
}

# Wait until the cert-manager CRDs are installed.
function wait_for_cert_manager_crds() {
    echo "Waiting for cert-manager CRDs to be installed..."
    local timeout=120
    local interval=5
    local elapsed=0

    until $OC get crd certificates.cert-manager.io >/dev/null 2>&1; do
        if [ $elapsed -ge $timeout ]; then
            echo "Timeout waiting for cert-manager CRDs"
            exit 1
        fi
        sleep $interval
        elapsed=$((elapsed + interval))
    done
    echo "cert-manager CRDs are installed."
}

# Wait until all cert-manager deployments are available.
function wait_for_cert_manager_ready() {
    echo "Waiting for cert-manager components to be ready..."
    local deployments=(cert-manager cert-manager-cainjector cert-manager-webhook)
    for dep in "${deployments[@]}"; do
        echo "Waiting for deployment '$dep'..."
        if ! $OC wait --for=condition=Available deployment/"$dep" -n cert-manager --timeout=300s; then
            echo "Timeout waiting for deployment '$dep' to become available."
            exit 1
        fi
    done
    echo "All cert-manager components are ready."
}

function wait_for_cert_manager_csv() {
    echo "Waiting for cert-manager Operator CSV to reach Succeeded status..."
    local timeout=300
    local interval=10
    local elapsed=0
    local csv_namespace="cert-manager-operator"
    while true; do
        local status
        status=$($OC get csv -n "$csv_namespace" -o jsonpath='{.items[0].status.phase}' 2>/dev/null || echo "NotFound")
        if [ "$status" = "Succeeded" ]; then
            echo "cert-manager Operator CSV is Succeeded."
            break
        fi
        if [ $elapsed -ge $timeout ]; then
            echo "Timeout waiting for cert-manager Operator CSV to succeed."
            exit 1
        fi
        sleep $interval
        elapsed=$((elapsed + interval))
    done
}

function collect_logs {
    if [ ! -d "$ARTIFACTS" ]; then
        mkdir -p "$ARTIFACTS"
    fi
    $OC get deployments -n kueue-system -o yaml > "$ARTIFACTS/kueue-deployment.yaml" || true
    $OC describe pods -n kueue-system > "$ARTIFACTS/kueue-system-pods.log" || true
    $OC logs -n kueue-system -l app=kueue --tail=-1 > "$ARTIFACTS/kueue-system-logs.log" || true
    $OC get events -n kueue-system "$ARTIFACTS/kueue-events.log" || true
    restore_ocp_manager_image
    restore_kueue_namespace
}

function deploy_kueue {
    local namespace=${KUEUE_NAMESPACE:-kueue-system}
    (cd config/components/manager-ocp && $KUSTOMIZE edit set image controller="$IMAGE_TAG" && \
     $KUSTOMIZE edit set namespace "$namespace")
    
    # Deploy kueue
    local kustomize_path namespace
    kustomize_path="config/default-ocp"

    # Set namespace in the default-ocp Kustomization
    (cd "${kustomize_path}" && \
        $KUSTOMIZE edit set namespace "$namespace")

    namespace="${KUEUE_NAMESPACE:-}"
    
    if [[ -n "$namespace" ]]; then
        # Create a namespace if it doesn't exist
        $OC create namespace "$namespace" --dry-run=client -o yaml | $OC apply -f -
        
        # Apply resources with namespace override
        $KUSTOMIZE build "${kustomize_path}" | $OC apply --server-side -f -
    else
        # Apply directly without modifications
        $OC apply --server-side -k "${kustomize_path}"
    fi
}

function restore_ocp_manager_image {
    (cd config/components/manager-ocp && $KUSTOMIZE edit set image controller="$INITIAL_IMAGE")
}

function restore_kueue_namespace {
    (cd config/default-ocp  && $KUSTOMIZE edit set namespace "$DEFAULT_NAMESPACE")
}

function allow_privileged_access {
    $OC adm policy add-scc-to-group privileged system:authenticated system:serviceaccounts
    $OC adm policy add-scc-to-group anyuid system:authenticated system:serviceaccounts
}

function deploy_cert_manager {
    echo "Deploying cert-manager..."
    ${SOURCE_DIR}/deploy-cert-manager-ocp.sh
    wait_for_cert_manager_crds
    wait_for_cert_manager_csv
    wait_for_cert_manager_ready
}

trap collect_logs EXIT
skips=(
        # do not deploy AppWrapper in OCP
        AppWrapper
        # do not deploy PyTorch in OCP
        PyTorch
        # do not deploy JobSet in OCP
        JobSet
        # do not deploy LWS in OCP
        LeaderWorkerSet
        # do not deploy Jax in OCP
        JAX
        # do not deploy KubeRay in OCP
        Kuberay
        # metrics setup is different than our OCP setup
        Metrics
        # ring -> we do not enable Fair sharing by default in our operator
        Fair
        # we do not enable this feature in our operator
        TopologyAwareScheduling
        # we do not enable VisibilityOnDemand in our operator
        "Kueue visibility server"
        # relies on particular CPU setup to force pods to not schedule
        "Failed Pod can be replaced in group"
        # relies on particular CPU setup
        "should allow to schedule a group of diverse pods"
        # relies on particular CPU setup.
        "StatefulSet created with WorkloadPriorityClass"
        # For tests that rely on CPU setup, we need to fix upstream to get cpu allocatables from node
        # rather than hardcoding CPU limits.
)
skipsRegex=$(
        IFS="|"
        printf "%s" "${skips[*]}"
)

GINKGO_SKIP_PATTERN="($skipsRegex)"
if [ "$SKIP_DEPLOY" != "true" ]; then
    deploy_cert_manager
    sleep 2m
    deploy_kueue
fi

# Label two worker nodes for e2e tests (similar to the Kind setup).
label_worker_nodes

# Disable scc rules for e2e pod tests
allow_privileged_access

$GINKGO $GINKGO_ARGS \
  --skip="${GINKGO_SKIP_PATTERN}" \
  --junit-report=junit.xml \
  --json-report=e2e.json \
  --output-dir="$ARTIFACTS" \
  -v ./test/e2e/$E2E_TARGET_FOLDER/...
