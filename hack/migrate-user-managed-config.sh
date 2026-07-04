#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  hack/migrate-user-managed-config.sh -n <namespace> [--claw <name>] [--kubectl <cmd>] [--dry-run]

Migrates an existing Claw instance to spec.config.management=user.

The migration is idempotent:
  1. Patches the Claw CR to spec.config.management=user.
  2. Restarts and waits for the gateway Deployment so init-config runs.
  3. Removes stale operator-injected platform/kubernetes skills if they remain.
  4. Reports user-owned files that may still contain operator-managed wording.

Environment:
  NS        Namespace when -n is omitted.
  CLAW      Claw resource name. Defaults to "instance".
  KUBECTL   kubectl-compatible command. Defaults to oc, then kubectl.
  DRY_RUN   Set to 1 to print commands without changing the cluster.

Options:
  --no-restart   Patch the CR but do not restart/wait for the gateway Deployment.
  --no-cleanup   Do not exec into the pod to remove stale operator skills.
EOF
}

namespace="${NS:-}"
claw="${CLAW:-instance}"
kubectl_cmd="${KUBECTL:-}"
dry_run="${DRY_RUN:-0}"
restart=1
cleanup=1

while [[ $# -gt 0 ]]; do
  case "$1" in
    -n|--namespace)
      namespace="${2:-}"
      shift 2
      ;;
    --claw)
      claw="${2:-}"
      shift 2
      ;;
    --kubectl)
      kubectl_cmd="${2:-}"
      shift 2
      ;;
    --dry-run)
      dry_run=1
      shift
      ;;
    --no-restart)
      restart=0
      shift
      ;;
    --no-cleanup)
      cleanup=0
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "$namespace" ]]; then
  echo "namespace is required (-n <namespace> or NS=<namespace>)" >&2
  usage >&2
  exit 2
fi

if [[ -z "$kubectl_cmd" ]]; then
  if command -v oc >/dev/null 2>&1; then
    kubectl_cmd=oc
  elif command -v kubectl >/dev/null 2>&1; then
    kubectl_cmd=kubectl
  else
    echo "neither oc nor kubectl was found; set KUBECTL=<cmd>" >&2
    exit 2
  fi
fi

run() {
  echo "+ $*"
  if [[ "$dry_run" == "1" ]]; then
    return 0
  fi
  "$@"
}

echo "Migrating Claw $namespace/$claw to user-managed runtime config"

current="$("$kubectl_cmd" get claw "$claw" -n "$namespace" -o jsonpath='{.spec.config.management}' 2>/dev/null || true)"
if [[ -z "$current" ]]; then
  current="operator"
fi
echo "Current spec.config.management: $current"

run "$kubectl_cmd" patch claw "$claw" -n "$namespace" --type merge \
  -p '{"spec":{"config":{"management":"user"}}}'

if [[ "$restart" == "1" ]]; then
  run "$kubectl_cmd" rollout restart "deployment/$claw" -n "$namespace"
  run "$kubectl_cmd" rollout status "deployment/$claw" -n "$namespace" --timeout=300s
  run "$kubectl_cmd" wait --for=condition=Ready "claw/$claw" -n "$namespace" --timeout=300s
fi

if [[ "$cleanup" == "1" ]]; then
  cleanup_script='
set -eu
root=/home/node/.openclaw
rm -rf "$root/workspace/skills/platform" "$root/workspace/skills/kubernetes"
echo "Removed stale operator-injected skills if present:"
echo "  $root/workspace/skills/platform"
echo "  $root/workspace/skills/kubernetes"

echo
echo "Review candidates for old operator-managed wording:"
for f in "$root/workspace/AGENTS.md" "$root/skills/deployment/SKILL.md"; do
  if [ ! -f "$f" ]; then
    echo "  missing: $f"
    continue
  fi
  if grep -E "Claw CR manages|operator-managed|Provider credentials, channels, MCP servers|spec.config.raw" "$f" >/dev/null 2>&1; then
    echo "  review: $f"
  else
    echo "  ok: $f"
  fi
done
'
  run "$kubectl_cmd" exec -n "$namespace" "deployment/$claw" -c gateway -- sh -c "$cleanup_script"
fi

echo
echo "Migration complete for $namespace/$claw."
echo "Runtime config is now user-owned after first boot. Continue making provider/model/MCP/plugin/skill changes through OpenClaw."
