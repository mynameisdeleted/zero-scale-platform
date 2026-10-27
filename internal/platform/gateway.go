package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// GatewayOptions configure the gateway manifests.
type GatewayOptions struct {
	Namespace    string
	Image        string
	Replicas     int
	RWXClass     string
	VolumeSize   string
	DataSize     string // size of the shared app-volumes PVC at /srv/zero-scale-volumes
	IdleTimeout  string
	ImagePullPol string
}

// GatewayManifest renders the gateway's ServiceAccount, RBAC, PVC, Deployment,
// Service and PodDisruptionBudget.
func GatewayManifest(o GatewayOptions) (string, error) {
	if o.Image == "" {
		return "", fmt.Errorf("--image is required (the image built by 'make image', pushed to a registry the cluster can pull from)")
	}
	if o.Namespace == "" {
		o.Namespace = DefaultNamespace
	}
	if o.Replicas == 0 {
		o.Replicas = 2
	}
	if o.VolumeSize == "" {
		o.VolumeSize = "5Gi"
	}
	if o.DataSize == "" {
		o.DataSize = "20Gi"
	}
	if o.IdleTimeout == "" {
		o.IdleTimeout = "60s"
	}
	if o.ImagePullPol == "" {
		o.ImagePullPol = "IfNotPresent"
	}
	if o.RWXClass == "" {
		return "", fmt.Errorf("no RWX StorageClass for /opt/apps; run 'zero-scale-ctl init' or 'zero-scale-ctl install longhorn', or pass --rwx-class")
	}
	// With one replica, minAvailable: 1 would block every node drain.
	pdb := "minAvailable: 1"
	if o.Replicas < 2 {
		pdb = "maxUnavailable: 1"
	}
	return fmt.Sprintf(`apiVersion: v1
kind: ServiceAccount
metadata:
  name: zero-scale-gateway
  namespace: %[1]s
  labels:
    app: zero-scale-gateway
    app.kubernetes.io/name: zero-scale-gateway
    app.kubernetes.io/part-of: zero-scale-platform
    app.kubernetes.io/managed-by: zero-scale-ctl
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: zero-scale-gateway-role
  namespace: %[1]s
  labels:
    app: zero-scale-gateway
    app.kubernetes.io/name: zero-scale-gateway
    app.kubernetes.io/part-of: zero-scale-platform
    app.kubernetes.io/managed-by: zero-scale-ctl
rules:
  - apiGroups: [""]
    resources: ["configmaps", "secrets", "pods"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: zero-scale-gateway-binding
  namespace: %[1]s
  labels:
    app: zero-scale-gateway
    app.kubernetes.io/name: zero-scale-gateway
    app.kubernetes.io/part-of: zero-scale-platform
    app.kubernetes.io/managed-by: zero-scale-ctl
subjects:
  - kind: ServiceAccount
    name: zero-scale-gateway
    namespace: %[1]s
roleRef:
  kind: Role
  name: zero-scale-gateway-role
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: zero-scale-binaries-pvc
  namespace: %[1]s
  labels:
    app: zero-scale-gateway
    app.kubernetes.io/name: zero-scale-gateway
    app.kubernetes.io/part-of: zero-scale-platform
    app.kubernetes.io/managed-by: zero-scale-ctl
spec:
  accessModes: ["ReadWriteMany"]
  storageClassName: %[4]s
  resources:
    requests:
      storage: %[5]s
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: zero-scale-volumes-pvc
  namespace: %[1]s
  labels:
    app: zero-scale-gateway
    app.kubernetes.io/name: zero-scale-gateway
    app.kubernetes.io/part-of: zero-scale-platform
    app.kubernetes.io/managed-by: zero-scale-ctl
spec:
  accessModes: ["ReadWriteMany"]
  storageClassName: %[4]s
  resources:
    requests:
      storage: %[8]s
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: zero-scale-gateway
  namespace: %[1]s
  labels:
    app: zero-scale-gateway
    app.kubernetes.io/name: zero-scale-gateway
    app.kubernetes.io/part-of: zero-scale-platform
    app.kubernetes.io/managed-by: zero-scale-ctl
spec:
  replicas: %[3]d
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 0
      maxSurge: 1
  selector:
    matchLabels:
      app: zero-scale-gateway
  template:
    metadata:
      labels:
        app: zero-scale-gateway
        app.kubernetes.io/name: zero-scale-gateway
        app.kubernetes.io/part-of: zero-scale-platform
        app.kubernetes.io/managed-by: zero-scale-ctl
    spec:
      serviceAccountName: zero-scale-gateway
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: ScheduleAnyway
          labelSelector:
            matchLabels:
              app: zero-scale-gateway
      containers:
        - name: systemd-runner
          image: %[2]s
          imagePullPolicy: %[7]s
          ports:
            - name: http
              containerPort: 80
          env:
            - name: POD_NAME
              valueFrom: {fieldRef: {fieldPath: metadata.name}}
            - name: POD_NAMESPACE
              valueFrom: {fieldRef: {fieldPath: metadata.namespace}}
            - name: IDLE_TIMEOUT
              value: "%[6]s"
          readinessProbe:
            tcpSocket: {port: 80}
            initialDelaySeconds: 5
            periodSeconds: 5
          securityContext:
            privileged: true
          volumeMounts:
            - {name: run, mountPath: /run}
            - {name: run-lock, mountPath: /run/lock}
            - {name: apps-volume, mountPath: /opt/apps}
            - {name: app-data, mountPath: /srv/zero-scale-volumes}
      volumes:
        - name: run
          emptyDir: {medium: Memory}
        - name: run-lock
          emptyDir: {medium: Memory}
        - name: apps-volume
          persistentVolumeClaim:
            claimName: zero-scale-binaries-pvc
        - name: app-data
          persistentVolumeClaim:
            claimName: zero-scale-volumes-pvc
---
apiVersion: v1
kind: Service
metadata:
  name: zero-scale-gateway
  namespace: %[1]s
  labels:
    app: zero-scale-gateway
    app.kubernetes.io/name: zero-scale-gateway
    app.kubernetes.io/part-of: zero-scale-platform
    app.kubernetes.io/managed-by: zero-scale-ctl
spec:
  selector:
    app: zero-scale-gateway
  ports:
    - name: http
      port: 80
      targetPort: 80
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: zero-scale-gateway
  namespace: %[1]s
  labels:
    app: zero-scale-gateway
    app.kubernetes.io/name: zero-scale-gateway
    app.kubernetes.io/part-of: zero-scale-platform
    app.kubernetes.io/managed-by: zero-scale-ctl
spec:
  %[9]s
  selector:
    matchLabels:
      app: zero-scale-gateway
`, o.Namespace, o.Image, o.Replicas, o.RWXClass, o.VolumeSize, o.IdleTimeout, o.ImagePullPol, o.DataSize, pdb), nil
}

var hostRe = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// ValidateHost accepts example.com, app.example.com and *.apps.example.com.
func ValidateHost(h string) error {
	if !hostRe.MatchString(h) {
		return fmt.Errorf("invalid host %q (want app.example.com or *.apps.example.com)", h)
	}
	return nil
}

// HostCovered reports whether host is served by one of the routed entries:
// an exact match, or a wildcard covering exactly one extra label.
func HostCovered(host string, routed []string) bool {
	host = strings.ToLower(host)
	for _, r := range routed {
		r = strings.ToLower(r)
		if r == host {
			return true
		}
		if suffix, ok := strings.CutPrefix(r, "*."); ok {
			if label, rest, found := strings.Cut(host, "."); found && label != "" && rest == suffix {
				return true
			}
		}
	}
	return false
}

// RouteName is the Ingress name for a routed host.
func RouteName(host string) string {
	prefix := "zero-scale-host-"
	if strings.HasPrefix(host, "*.") {
		prefix = "zero-scale-wildcard-"
		host = strings.TrimPrefix(host, "*.")
	}
	name := prefix + strings.ReplaceAll(host, ".", "-")
	if len(name) > 63 {
		name = name[:54] + "-" + sha8(host)
	}
	return strings.TrimRight(name, "-")
}

// RouteManifest renders the Ingress that sends host to the gateway Service.
// tls is "", "secret:<name>" or "issuer:<ClusterIssuer>".
func RouteManifest(host, namespace, service, class, tls string) (string, error) {
	if err := ValidateHost(host); err != nil {
		return "", err
	}
	name := RouteName(host)
	var b strings.Builder
	fmt.Fprintf(&b, `apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: %s
  namespace: %s
  labels:
    app: zero-scale-gateway
    app.kubernetes.io/part-of: zero-scale-platform
    zero-scale-route: "true"
  annotations:
    zero-scale.io/host: %q
    nginx.ingress.kubernetes.io/upstream-hash-by: "$host"
    nginx.ingress.kubernetes.io/proxy-body-size: "50m"
`, name, namespace, host)
	secret := ""
	switch {
	case strings.HasPrefix(tls, "issuer:"):
		fmt.Fprintf(&b, "    cert-manager.io/cluster-issuer: %q\n", strings.TrimPrefix(tls, "issuer:"))
		secret = name + "-tls"
	case strings.HasPrefix(tls, "secret:"):
		secret = strings.TrimPrefix(tls, "secret:")
	case tls != "":
		return "", fmt.Errorf("tls must be secret:<name> or issuer:<name>, got %q", tls)
	}
	b.WriteString("spec:\n")
	if class != "" {
		fmt.Fprintf(&b, "  ingressClassName: %s\n", class)
	}
	if secret != "" {
		fmt.Fprintf(&b, "  tls:\n    - hosts: [%q]\n      secretName: %s\n", host, secret)
	}
	fmt.Fprintf(&b, `  rules:
    - host: %q
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: %s
                port:
                  number: 80
`, host, service)
	return b.String(), nil
}

// IssuerSupportsWildcard checks a cert-manager ClusterIssuer (JSON) can issue a
// wildcard certificate: non-ACME issuers can, ACME issuers need a DNS-01 solver.
func IssuerSupportsWildcard(issuerJSON []byte) (bool, error) {
	items, err := parseObject(issuerJSON)
	if err != nil {
		return false, err
	}
	if items.get("spec", "acme") == nil {
		return true, nil
	}
	for _, s := range items.list("spec", "acme", "solvers") {
		if s.get("dns01") != nil {
			return true, nil
		}
	}
	return false, nil
}

func sha8(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}
