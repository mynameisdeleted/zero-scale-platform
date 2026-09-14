#!/usr/bin/env bash
set -eo pipefail

NAMESPACE="${NAMESPACE:-default}"
GATEWAY_POD_LABEL="app.kubernetes.io/name=zero-scale-platform"

function usage() {
  cat <<EOF
Zero-Scale Platform Management CLI

Usage:
  $0 [command] [options]

Commands:
  deploy-app         Deploy or update a zero-scale application
  list-apps          List all configured zero-scale applications
  delete-app <name>  Delete an application ConfigMap
  status <host>      Check the systemd status of an app inside the gateway
  logs <host>        View the systemd journal logs of an app inside the gateway
  install-chart      Install or upgrade the full Helm chart

Options for deploy-app:
  --name <name>          Application identifier (e.g., bdash2) [Required]
  --host <host>          Hostname to route (e.g., bdash2.kube.fairfaxmedia.net) [Required]
  --url <url>            Binary download URL (S3 / MinIO / HTTP) [Required]
  --type <elf|wasm>      Runtime type (default: elf)
  --db                   Enable automated PostgreSQL schema provisioning
  --mount <path>         Container directory to bind-mount from Longhorn (e.g. /app/uploads)
  --no-scale-to-zero     Keep application running warm permanently

Examples:
  # Deploy a new application
  $0 deploy-app --name bdash2 --host bdash2.kube.fairfaxmedia.net --url http://minio.minio-operator/binaries/bdash-x86_64 --db --mount /app/uploads

  # List active apps
  $0 list-apps

  # Check logs of a running systemd unit
  $0 logs bdash2.kube.fairfaxmedia.net
EOF
  exit 1
}

function get_gateway_pod() {
  local pod
  pod=$(kubectl get pods -n "$NAMESPACE" -l "$GATEWAY_POD_LABEL" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  if [ -z "$pod" ]; then
    pod=$(kubectl get pods -n "$NAMESPACE" -l app=zero-scale-gateway -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  fi
  if [ -z "$pod" ]; then
    echo "Error: No running zero-scale gateway pod found in namespace $NAMESPACE" >&2
    exit 1
  fi
  echo "$pod"
}

case "$1" in
  deploy-app)
    shift
    NAME=""
    HOST=""
    URL=""
    TYPE="elf"
    DB="false"
    MOUNTS=""
    SCALE_TO_ZERO="true"

    while [[ $# -gt 0 ]]; do
      case "$1" in
        --name) NAME="$2"; shift 2 ;;
        --host) HOST="$2"; shift 2 ;;
        --url) URL="$2"; shift 2 ;;
        --type) TYPE="$2"; shift 2 ;;
        --db) DB="true"; shift 1 ;;
        --mount) MOUNTS="$2"; shift 2 ;;
        --no-scale-to-zero) SCALE_TO_ZERO="false"; shift 1 ;;
        *) echo "Unknown option: $1"; usage ;;
      esac
    done

    if [ -z "$NAME" ] || [ -z "$HOST" ] || [ -z "$URL" ]; then
      echo "Error: --name, --host, and --url are required." >&2
      exit 1
    fi

    echo "==> Applying ConfigMap zero-scale-app-${NAME}..."
    cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: zero-scale-app-${NAME}
  namespace: ${NAMESPACE}
  labels:
    zero-scale-app: "true"
data:
  host: "${HOST}"
  url: "${URL}"
  type: "${TYPE}"
$( [ "$DB" = "true" ] && echo "  database: \"true\"" )
$( [ -n "$MOUNTS" ] && echo "  mounts: \"${MOUNTS}\"" )
$( [ "$SCALE_TO_ZERO" = "false" ] && echo "  scale-to-zero: \"false\"" )
EOF
    echo "==> Application ${NAME} deployed successfully for host ${HOST}."
    ;;

  list-apps)
    echo "==> Configured Zero-Scale Applications in namespace '$NAMESPACE':"
    kubectl get configmaps -n "$NAMESPACE" --show-labels | grep "zero-scale-app" || echo "No zero-scale applications found."
    ;;

  delete-app)
    NAME="$2"
    if [ -z "$NAME" ]; then
      echo "Usage: $0 delete-app <name>" >&2
      exit 1
    fi
    echo "==> Deleting ConfigMap zero-scale-app-${NAME}..."
    kubectl delete configmap -n "$NAMESPACE" "zero-scale-app-${NAME}"
    ;;

  status)
    HOST="$2"
    if [ -z "$HOST" ]; then
      echo "Usage: $0 status <host>" >&2
      exit 1
    fi
    POD=$(get_gateway_pod)
    UNIT="app-${HOST}"
    echo "==> Checking status of ${UNIT} in pod ${POD}..."
    kubectl exec -n "$NAMESPACE" "$POD" -- systemctl status "$UNIT" || true
    ;;

  logs)
    HOST="$2"
    if [ -z "$HOST" ]; then
      echo "Usage: $0 logs <host>" >&2
      exit 1
    fi
    POD=$(get_gateway_pod)
    UNIT="app-${HOST}"
    echo "==> Fetching journal logs for ${UNIT} in pod ${POD}..."
    kubectl exec -n "$NAMESPACE" "$POD" -- journalctl -u "$UNIT" --no-pager -n 50
    ;;

  install-chart)
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    CHART_DIR="${SCRIPT_DIR}/zero-scale-chart"
    echo "==> Installing / upgrading zero-scale-platform Helm chart from ${CHART_DIR}..."
    helm upgrade --install zero-scale-platform "$CHART_DIR" -n "$NAMESPACE"
    ;;

  *)
    usage
    ;;
esac
