#!/bin/bash
# dev-deploy.sh — Build and redeploy CubeSandbox components on this dev machine.
#
# Usage:
#   ./dev-deploy.sh              # Build ALL components, then deploy
#   ./dev-deploy.sh cubeapi      # Build & deploy only CubeAPI
#   ./dev-deploy.sh shim         # Build & deploy only CubeShim
#   ./dev-deploy.sh cubelet      # Build & deploy only Cubelet
#   ./dev-deploy.sh cubemaster   # Build & deploy only CubeMaster
#   ./dev-deploy.sh network-agent # Build & deploy only network-agent
#   ./dev-deploy.sh --deploy-only # Skip build, deploy existing _output/bin artifacts
#
# Multiple targets:
#   ./dev-deploy.sh cubeapi cubelet shim

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# ─── Paths ────────────────────────────────────────────────────────────────────
TOOLBOX="/usr/local/services/cubetoolbox"
OUTPUT="$SCRIPT_DIR/_output/bin"

# ─── Colors ───────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

info()  { echo -e "${GREEN}[deploy]${NC} $*"; }
warn()  { echo -e "${YELLOW}[deploy]${NC} $*"; }
error() { echo -e "${RED}[deploy]${NC} $*" >&2; }

# ─── Deploy functions ─────────────────────────────────────────────────────────

deploy_cubeapi() {
    info "Deploying CubeAPI..."
    systemctl stop cube-sandbox-cube-api.service
    cp "$OUTPUT/cube-api" "$TOOLBOX/CubeAPI/bin/cube-api"
    chmod +x "$TOOLBOX/CubeAPI/bin/cube-api"
    systemctl start cube-sandbox-cube-api.service
    info "CubeAPI deployed ✓"
}

deploy_cubelet() {
    info "Deploying Cubelet..."
    systemctl stop cube-sandbox-cubelet.service
    cp "$OUTPUT/cubelet" "$TOOLBOX/Cubelet/bin/cubelet"
    cp "$OUTPUT/cubecli" "$TOOLBOX/Cubelet/bin/cubecli"
    chmod +x "$TOOLBOX/Cubelet/bin/cubelet" "$TOOLBOX/Cubelet/bin/cubecli"
    systemctl start cube-sandbox-cubelet.service
    info "Cubelet + CubeCLI deployed ✓"
}

deploy_shim() {
    info "Deploying CubeShim..."
    # Shim is spawned by cubelet on demand; stop cubelet to ensure no active shims
    systemctl stop cube-sandbox-cubelet.service
    cp "$OUTPUT/containerd-shim-cube-rs" "$TOOLBOX/cube-shim/bin/containerd-shim-cube-rs"
    cp "$OUTPUT/cube-runtime" "$TOOLBOX/cube-shim/bin/cube-runtime"
    chmod +x "$TOOLBOX/cube-shim/bin/containerd-shim-cube-rs" "$TOOLBOX/cube-shim/bin/cube-runtime"
    systemctl start cube-sandbox-cubelet.service
    info "CubeShim + cube-runtime deployed ✓"
    warn "Note: Already-running sandboxes still use the old shim process."
}

deploy_cubemaster() {
    info "Deploying CubeMaster..."
    systemctl stop cube-sandbox-cubemaster.service
    cp "$OUTPUT/cubemaster" "$TOOLBOX/CubeMaster/bin/cubemaster"
    cp "$OUTPUT/cubemastercli" "$TOOLBOX/CubeMaster/bin/cubemastercli"
    chmod +x "$TOOLBOX/CubeMaster/bin/cubemaster" "$TOOLBOX/CubeMaster/bin/cubemastercli"
    systemctl start cube-sandbox-cubemaster.service
    info "CubeMaster deployed ✓"
}

deploy_network_agent() {
    info "Deploying network-agent..."
    systemctl stop cube-sandbox-network-agent.service
    cp "$OUTPUT/network-agent" "$TOOLBOX/network-agent/bin/network-agent"
    chmod +x "$TOOLBOX/network-agent/bin/network-agent"
    systemctl start cube-sandbox-network-agent.service
    info "network-agent deployed ✓"
}

# ─── Build functions ──────────────────────────────────────────────────────────

build_cubeapi() {
    info "Building CubeAPI..."
    make cubeapi
    info "CubeAPI build done ✓"
}

build_cubelet() {
    info "Building Cubelet..."
    make cubelet
    info "Cubelet build done ✓"
}

build_shim() {
    info "Building CubeShim..."
    make shim
    info "CubeShim build done ✓"
}

build_cubemaster() {
    info "Building CubeMaster..."
    make cubemaster
    info "CubeMaster build done ✓"
}

build_network_agent() {
    info "Building network-agent..."
    make network-agent
    info "network-agent build done ✓"
}

# ─── Main logic ───────────────────────────────────────────────────────────────

DEPLOY_ONLY=false
TARGETS=()

for arg in "$@"; do
    case "$arg" in
        --deploy-only) DEPLOY_ONLY=true ;;
        cubeapi|shim|cubelet|cubemaster|network-agent) TARGETS+=("$arg") ;;
        all) TARGETS=(cubeapi shim cubelet cubemaster network-agent) ;;
        *) error "Unknown target: $arg"; exit 1 ;;
    esac
done

# Default: build and deploy all
if [ ${#TARGETS[@]} -eq 0 ]; then
    TARGETS=(cubeapi shim cubelet)
    warn "No targets specified, defaulting to: ${TARGETS[*]}"
fi

# Deduplicate: if both shim and cubelet are targets, handle cubelet restart once
HAS_SHIM=false
HAS_CUBELET=false
for t in "${TARGETS[@]}"; do
    [[ "$t" == "shim" ]] && HAS_SHIM=true
    [[ "$t" == "cubelet" ]] && HAS_CUBELET=true
done

info "=========================================="
info "  CubeSandbox Dev Deploy"
info "  Targets: ${TARGETS[*]}"
info "  Deploy-only: $DEPLOY_ONLY"
info "=========================================="
echo

# ─── Build phase ──────────────────────────────────────────────────────────────
if [ "$DEPLOY_ONLY" = false ]; then
    info ">>> Build phase"
    for target in "${TARGETS[@]}"; do
        "build_${target//-/_}"
    done
    echo
fi

# ─── Verify artifacts exist ───────────────────────────────────────────────────
info ">>> Checking build artifacts..."
MISSING=false
for target in "${TARGETS[@]}"; do
    case "$target" in
        cubeapi)
            [[ -f "$OUTPUT/cube-api" ]] || { error "Missing: $OUTPUT/cube-api"; MISSING=true; } ;;
        cubelet)
            [[ -f "$OUTPUT/cubelet" ]] || { error "Missing: $OUTPUT/cubelet"; MISSING=true; }
            [[ -f "$OUTPUT/cubecli" ]] || { error "Missing: $OUTPUT/cubecli"; MISSING=true; } ;;
        shim)
            [[ -f "$OUTPUT/containerd-shim-cube-rs" ]] || { error "Missing: $OUTPUT/containerd-shim-cube-rs"; MISSING=true; }
            [[ -f "$OUTPUT/cube-runtime" ]] || { error "Missing: $OUTPUT/cube-runtime"; MISSING=true; } ;;
        cubemaster)
            [[ -f "$OUTPUT/cubemaster" ]] || { error "Missing: $OUTPUT/cubemaster"; MISSING=true; } ;;
        network-agent)
            [[ -f "$OUTPUT/network-agent" ]] || { error "Missing: $OUTPUT/network-agent"; MISSING=true; } ;;
    esac
done
if [ "$MISSING" = true ]; then
    error "Some artifacts are missing. Run without --deploy-only to build first."
    exit 1
fi
info "All artifacts present ✓"
echo

# ─── Deploy phase ────────────────────────────────────────────────────────────
info ">>> Deploy phase"

# If both shim and cubelet are targeted, deploy them together (single cubelet restart)
if [ "$HAS_SHIM" = true ] && [ "$HAS_CUBELET" = true ]; then
    info "Deploying Shim + Cubelet together (single cubelet restart)..."
    systemctl stop cube-sandbox-cubelet.service
    cp "$OUTPUT/containerd-shim-cube-rs" "$TOOLBOX/cube-shim/bin/containerd-shim-cube-rs"
    cp "$OUTPUT/cube-runtime" "$TOOLBOX/cube-shim/bin/cube-runtime"
    chmod +x "$TOOLBOX/cube-shim/bin/containerd-shim-cube-rs" "$TOOLBOX/cube-shim/bin/cube-runtime"
    cp "$OUTPUT/cubelet" "$TOOLBOX/Cubelet/bin/cubelet"
    cp "$OUTPUT/cubecli" "$TOOLBOX/Cubelet/bin/cubecli"
    chmod +x "$TOOLBOX/Cubelet/bin/cubelet" "$TOOLBOX/Cubelet/bin/cubecli"
    systemctl start cube-sandbox-cubelet.service
    info "Shim + Cubelet deployed ✓"

    # Deploy remaining targets
    for target in "${TARGETS[@]}"; do
        [[ "$target" == "shim" || "$target" == "cubelet" ]] && continue
        "deploy_${target//-/_}"
    done
else
    for target in "${TARGETS[@]}"; do
        "deploy_${target//-/_}"
    done
fi

echo
info "=========================================="
info "  All done! Services restarted."
info "=========================================="

# Show final service status
echo
systemctl --no-pager status cube-sandbox-cube-api cube-sandbox-cubelet cube-sandbox-cubemaster cube-sandbox-network-agent 2>/dev/null | grep -E "●|Active:" || true
