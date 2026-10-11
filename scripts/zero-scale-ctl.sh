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
  apply              Deploy from simple 10-line config (creates DB, secret, config, binary, ingress)
  deploy-app         Deploy or update a zero-scale application
  upload-binary      Upload local binary to platform storage volume & create launcher
  provision-db       Provision database on shared operator & write k8s secret references
  list-apps          List all configured zero-scale applications
  delete-app <name>  Delete an application ConfigMap
  status <host>      Check the systemd status of an app inside the gateway
  logs <host>        View the systemd journal logs of an app inside the gateway
  install-chart      Install or upgrade the full Helm chart

Options for apply:
  -f, --file <file>      Path to 10-line application config file [Required]
  --expand <file>        Optional: output fully expanded Kubernetes YAML manifest

Options for deploy-app:
  --name <name>          Application identifier (e.g., bdash2) [Required]
  --host <host>          Hostname to route (e.g., bdash2.kube.fairfaxmedia.net) [Required]
  --url <url>            Binary download URL (S3 / MinIO / HTTP) [Required]
  --type <elf|wasm>      Runtime type (default: elf)
  --db                   Enable automated PostgreSQL schema provisioning
  --mount <path>         Container directory to bind-mount from Longhorn (e.g. /app/uploads)
  --no-scale-to-zero     Keep application running warm permanently

Options for upload-binary:
  --binary <path>        Path to local binary on host [Required]
  --name <name>          Destination binary name in /opt/apps (defaults to filename)
  --host <host>          Hostname to map or create launcher for (e.g., fdm.ffxm.cc)
  --wrapper <type>       Launcher wrapper type: 'fleet' (auto-adapts ports/migrations) or none (symlink)

Options for provision-db:
  --app <name>           Application identifier (e.g., fleetdm) [Required]
  --engine <mysql|pg>    Database engine: 'mysql' (default) or 'postgres'
  --database <dbname>    Database name (defaults to app name)
  --user <username>      Database username (defaults to app name)
  --password <password>  Database password (defaults to auto-generated)
  --out <file>           Path to YAML manifest to update with database references
  --host <host>          Hostname for newly generated YAML manifest (e.g. fdm.ffxm.cc)
  --url <url>            Binary download URL for newly generated YAML manifest

Examples:
  # Upload a local binary directly to the shared platform volume without docker push:
  $0 upload-binary --binary /path/to/fleet --name fleet --host fdm.ffxm.cc --wrapper fleet

  # Provision a shared MySQL database for FleetDM and output to app YAML:
  $0 provision-db --app fleetdm --engine mysql --host fdm.ffxm.cc --out /home/steve/Documents/terrapower/fleetdm/fleetdm.yaml

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

  provision-db)
    shift
    APP=""
    ENGINE="mysql"
    DB_NAME=""
    DB_USER=""
    DB_PASS=""
    OUT_FILE=""
    HOST=""
    URL=""

    while [[ $# -gt 0 ]]; do
      case "$1" in
        --app) APP="$2"; shift 2 ;;
        --engine) ENGINE="$2"; shift 2 ;;
        --database) DB_NAME="$2"; shift 2 ;;
        --user) DB_USER="$2"; shift 2 ;;
        --password) DB_PASS="$2"; shift 2 ;;
        --out) OUT_FILE="$2"; shift 2 ;;
        --host) HOST="$2"; shift 2 ;;
        --url) URL="$2"; shift 2 ;;
        *) echo "Unknown option: $1"; usage ;;
      esac
    done

    if [ -z "$APP" ]; then
      echo "Error: --app <name> is required." >&2
      exit 1
    fi

    DB_NAME="${DB_NAME:-${APP}}"
    DB_USER="${DB_USER:-${APP}}"
    if [ -z "$DB_PASS" ]; then
      DB_PASS=$(LC_ALL=C tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 20)
    fi

    echo "==> Provisioning database for application '${APP}' using engine '${ENGINE}'..."

    if [ "$ENGINE" = "mysql" ]; then
      MYSQL_POD=$(kubectl get pod -n "$NAMESPACE" -l app=shared-mysql --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
      if [ -z "$MYSQL_POD" ]; then
        echo "Error: shared-mysql pod is not running in namespace $NAMESPACE." >&2
        echo "Hint: Deploy it via 'kubectl apply -f deploy/shared-data-services.yaml -n $NAMESPACE'" >&2
        exit 1
      fi

      MYSQL_ROOT_PASS=$(kubectl get secret -n "$NAMESPACE" shared-mysql-admin -o jsonpath='{.data.MYSQL_ROOT_PASSWORD}' 2>/dev/null | base64 -d || true)
      if [ -z "$MYSQL_ROOT_PASS" ]; then
        echo "Error: Secret 'shared-mysql-admin' not found in namespace $NAMESPACE." >&2
        exit 1
      fi

      echo "--> Executing MySQL database and user provisioning on ${MYSQL_POD}..."
      kubectl exec -n "$NAMESPACE" "$MYSQL_POD" -- mysql -u root -p"${MYSQL_ROOT_PASS}" -e "
        CREATE DATABASE IF NOT EXISTS \`${DB_NAME}\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
        CREATE USER IF NOT EXISTS '${DB_USER}'@'%' IDENTIFIED BY '${DB_PASS}';
        ALTER USER '${DB_USER}'@'%' IDENTIFIED BY '${DB_PASS}';
        GRANT ALL PRIVILEGES ON \`${DB_NAME}\`.* TO '${DB_USER}'@'%';
        FLUSH PRIVILEGES;
      "

      CONFIG_MAP_NAME="${APP}-db-config"
      SECRET_NAME="${APP}-db-secret"

      echo "--> Creating ConfigMap ${CONFIG_MAP_NAME}..."
      cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${CONFIG_MAP_NAME}
  namespace: ${NAMESPACE}
  labels:
    zero-scale-db: "true"
    app: "${APP}"
data:
  DATABASE_HOST: "shared-mysql.${NAMESPACE}.svc.cluster.local"
  DATABASE_PORT: "3306"
  DATABASE_NAME: "${DB_NAME}"
  FLEET_MYSQL_ADDRESS: "shared-mysql.${NAMESPACE}.svc.cluster.local:3306"
  FLEET_MYSQL_DATABASE: "${DB_NAME}"
  FLEET_REDIS_ADDRESS: "shared-redis.${NAMESPACE}.svc.cluster.local:6379"
EOF

      echo "--> Creating Secret ${SECRET_NAME}..."
      cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: ${SECRET_NAME}
  namespace: ${NAMESPACE}
  labels:
    zero-scale-db: "true"
    app: "${APP}"
type: Opaque
stringData:
  DATABASE_USER: "${DB_USER}"
  DATABASE_PASSWORD: "${DB_PASS}"
  FLEET_MYSQL_USERNAME: "${DB_USER}"
  FLEET_MYSQL_PASSWORD: "${DB_PASS}"
EOF

    elif [ "$ENGINE" = "postgres" ]; then
      PG_POD=$(kubectl get pod -n "$NAMESPACE" -l "cnpg.io/cluster=ha-postgres,role=primary" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
      if [ -z "$PG_POD" ]; then
        PG_POD=$(kubectl get pod -n cnpg-system -l "cnpg.io/cluster=ha-postgres,role=primary" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
      fi
      if [ -z "$PG_POD" ]; then
        echo "Error: CloudNativePG primary pod (ha-postgres) not found in namespace $NAMESPACE or cnpg-system." >&2
        exit 1
      fi

      echo "--> Executing PostgreSQL database and user provisioning on ${PG_POD}..."
      kubectl exec -n "$NAMESPACE" "$PG_POD" -c postgres -- psql -U postgres -c "
        DO \$\$
        BEGIN
          IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = '${DB_USER}') THEN
            CREATE ROLE \"${DB_USER}\" WITH LOGIN PASSWORD '${DB_PASS}';
          ELSE
            ALTER ROLE \"${DB_USER}\" WITH PASSWORD '${DB_PASS}';
          END IF;
        END
        \$\$;
      "
      kubectl exec -n "$NAMESPACE" "$PG_POD" -c postgres -- psql -U postgres -tc "SELECT 1 FROM pg_database WHERE datname = '${DB_NAME}'" | grep -q 1 || \
        kubectl exec -n "$NAMESPACE" "$PG_POD" -c postgres -- psql -U postgres -c "CREATE DATABASE \"${DB_NAME}\" OWNER \"${DB_USER}\";"

      CONFIG_MAP_NAME="${APP}-db-config"
      SECRET_NAME="${APP}-db-secret"

      echo "--> Creating ConfigMap ${CONFIG_MAP_NAME}..."
      cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${CONFIG_MAP_NAME}
  namespace: ${NAMESPACE}
  labels:
    zero-scale-db: "true"
    app: "${APP}"
data:
  DATABASE_HOST: "ha-postgres-rw.${NAMESPACE}.svc.cluster.local"
  DATABASE_PORT: "5432"
  DATABASE_NAME: "${DB_NAME}"
  PGHOST: "ha-postgres-rw.${NAMESPACE}.svc.cluster.local"
  PGPORT: "5432"
  PGDATABASE: "${DB_NAME}"
EOF

      echo "--> Creating Secret ${SECRET_NAME}..."
      cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: ${SECRET_NAME}
  namespace: ${NAMESPACE}
  labels:
    zero-scale-db: "true"
    app: "${APP}"
type: Opaque
stringData:
  DATABASE_USER: "${DB_USER}"
  DATABASE_PASSWORD: "${DB_PASS}"
  PGUSER: "${DB_USER}"
  PGPASSWORD: "${DB_PASS}"
EOF

    else
      echo "Error: Unsupported database engine '${ENGINE}'. Choose 'mysql' or 'postgres'." >&2
      exit 1
    fi

    echo "==> Database '${DB_NAME}' and user '${DB_USER}' provisioned successfully."
    echo "    ConfigMap: ${CONFIG_MAP_NAME}"
    echo "    Secret:    ${SECRET_NAME}"

    if [ -n "$OUT_FILE" ]; then
      echo "--> Updating output YAML file '${OUT_FILE}' with database references..."
      mkdir -p "$(dirname "$OUT_FILE")"
      if [ -f "$OUT_FILE" ]; then
        if grep -q "database_configmap:" "$OUT_FILE"; then
          sed -i "s|database_configmap:.*|database_configmap: \"${CONFIG_MAP_NAME}\"|" "$OUT_FILE"
        else
          sed -i "/type:/a \  database_configmap: \"${CONFIG_MAP_NAME}\"" "$OUT_FILE"
        fi
        if grep -q "database_secret:" "$OUT_FILE"; then
          sed -i "s|database_secret:.*|database_secret: \"${SECRET_NAME}\"|" "$OUT_FILE"
        else
          sed -i "/database_configmap:/a \  database_secret: \"${SECRET_NAME}\"" "$OUT_FILE"
        fi
      else
        cat <<EOF > "$OUT_FILE"
apiVersion: v1
kind: ConfigMap
metadata:
  name: zero-scale-app-${APP}
  namespace: ${NAMESPACE}
  labels:
    zero-scale-app: "true"
data:
  host: "${HOST:-${APP}.ffxm.cc}"
  url: "${URL:-http://minio.minio-operator/binaries/${APP}}"
  type: "elf"
  database_configmap: "${CONFIG_MAP_NAME}"
  database_secret: "${SECRET_NAME}"
  scale-to-zero: "true"
EOF
      fi
      echo "==> Wrote updated zero-scale configuration to '${OUT_FILE}'."
    fi
    ;;

  upload-binary)
    shift
    NAME=""
    HOST=""
    BINARY=""
    WRAPPER=""

    while [[ $# -gt 0 ]]; do
      case "$1" in
        --name) NAME="$2"; shift 2 ;;
        --host) HOST="$2"; shift 2 ;;
        --binary) BINARY="$2"; shift 2 ;;
        --wrapper) WRAPPER="$2"; shift 2 ;;
        *) echo "Unknown option: $1"; usage ;;
      esac
    done

    if [ -z "$BINARY" ]; then
      echo "Error: --binary <path> is required." >&2
      exit 1
    fi
    if [ ! -f "$BINARY" ]; then
      echo "Error: Binary file '$BINARY' not found." >&2
      exit 1
    fi

    POD=$(get_gateway_pod)
    TARGET_NAME="${NAME:-$(basename "$BINARY")}"

    echo "==> Uploading binary '${BINARY}' to pod '${POD}:/opt/apps/${TARGET_NAME}'..."
    kubectl cp "${BINARY}" "${NAMESPACE}/${POD}:/opt/apps/${TARGET_NAME}" -c systemd-runner
    kubectl exec "${POD}" -n "${NAMESPACE}" -c systemd-runner -- chmod +x "/opt/apps/${TARGET_NAME}"
    echo "--> Successfully deployed binary to /opt/apps/${TARGET_NAME} on persistent volume."

    if [ -n "$HOST" ]; then
      if [ "$WRAPPER" = "fleet" ]; then
        echo "--> Creating zero-scale wrapper launcher for FleetDM at /opt/apps/${HOST}..."
        cat << 'WRAPPER_EOF' | kubectl exec -i "${POD}" -n "${NAMESPACE}" -c systemd-runner -- sh -c "cat > /opt/apps/${HOST} && chmod +x /opt/apps/${HOST}"
#!/bin/bash
# FleetDM Zero-Scale Launcher
PORT=8080
while [[ $# -gt 0 ]]; do
  case "$1" in
    -port|--port) PORT="$2"; shift 2 ;;
    *) shift ;;
  esac
done

export FLEET_SERVER_ADDRESS="0.0.0.0:${PORT}"
export FLEET_SERVER_TLS="false"
export FLEET_SERVER_URL="https://fdm.ffxm.cc"

# Ensure DB schema is prepared
/opt/apps/fleet prepare db --no-prompt 2>&1 || true

# Exec Fleet server
exec /opt/apps/fleet serve
WRAPPER_EOF
        echo "--> FleetDM launcher created at /opt/apps/${HOST}."
      else
        echo "--> Symlinking /opt/apps/${HOST} -> /opt/apps/${TARGET_NAME}..."
        kubectl exec "${POD}" -n "${NAMESPACE}" -c systemd-runner -- ln -sf "/opt/apps/${TARGET_NAME}" "/opt/apps/${HOST}"
      fi
    fi
    echo "==> Binary deployment complete. App is ready to serve zero-scale on host '${HOST:-${TARGET_NAME}}'."
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
