package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// RunKubectl executes a kubectl command with optional stdin input.
func RunKubectl(args []string, input string) (string, error) {
	cmd := exec.Command("kubectl", args...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("kubectl %s failed: %v, stderr: %s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}

// GetGatewayPod locates the running Zero-Scale Gateway pod.
func GetGatewayPod(namespace string) (string, error) {
	labels := []string{
		"app=zero-scale-gateway",
		"app.kubernetes.io/name=zero-scale-platform",
	}
	for _, label := range labels {
		out, err := RunKubectl([]string{
			"get", "pods", "-n", namespace,
			"-l", label,
			"--field-selector=status.phase=Running",
			"-o", "jsonpath={.items[0].metadata.name}",
		}, "")
		pod := strings.TrimSpace(out)
		if err == nil && pod != "" {
			return pod, nil
		}
	}
	return "", fmt.Errorf(`no running zero-scale gateway pod found in namespace '%s'
--> Remediation: Deploy the gateway with: make deploy-manifests (or kubectl apply -f deploy/ -n %s)
--> Note: If running on OpenShift, ensure the privileged SCC is granted:
    oc adm policy add-scc-to-user privileged -z default -n %s
--> Documentation: https://git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform#deploy-to-kubernetes`, namespace, namespace, namespace)
}

// ResourceExists checks if a Kubernetes resource exists.
func ResourceExists(kind, name, namespace string) bool {
	_, err := RunKubectl([]string{"get", kind, name, "-n", namespace}, "")
	return err == nil
}

// GetSecretData returns decoded string key-values from a Secret.
func GetSecretData(name, namespace string) (map[string]string, error) {
	out, err := RunKubectl([]string{"get", "secret", name, "-n", namespace, "-o", "json"}, "")
	if err != nil {
		return nil, err
	}
	var raw struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, err
	}
	result := make(map[string]string, len(raw.Data))
	for k, v := range raw.Data {
		decoded, err := base64.StdEncoding.DecodeString(v)
		if err == nil {
			result[k] = string(decoded)
		}
	}
	return result, nil
}

// GetConfigMapData returns string key-values from a ConfigMap.
func GetConfigMapData(name, namespace string) (map[string]string, error) {
	out, err := RunKubectl([]string{"get", "configmap", name, "-n", namespace, "-o", "json"}, "")
	if err != nil {
		return nil, err
	}
	return configMapDataFromJSON(out)
}

// configMapDataFromJSON extracts .data from `kubectl get configmap -o json` output.
func configMapDataFromJSON(out string) (map[string]string, error) {
	var raw struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, err
	}
	if raw.Data == nil {
		return make(map[string]string), nil
	}
	return raw.Data, nil
}

// ApplyYAML applies a YAML string via kubectl apply -f -.
func ApplyYAML(yamlContent, namespace string) error {
	_, err := RunKubectl([]string{"apply", "-n", namespace, "-f", "-"}, yamlContent)
	return err
}

// CreateOrUpdateSecret creates or updates a Kubernetes Secret.
func CreateOrUpdateSecret(name, namespace string, data map[string]string, labels map[string]string) error {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("apiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n  namespace: %s\n", name, namespace))
	if len(labels) > 0 {
		b.WriteString("  labels:\n")
		for k, v := range labels {
			b.WriteString(fmt.Sprintf("    %s: %q\n", k, v))
		}
	}
	b.WriteString("type: Opaque\nstringData:\n")
	for k, v := range data {
		b.WriteString(fmt.Sprintf("  %s: %q\n", k, v))
	}
	return ApplyYAML(b.String(), namespace)
}

// CreateOrUpdateConfigMap creates or updates a ConfigMap.
func CreateOrUpdateConfigMap(name, namespace string, data map[string]string, labels map[string]string) error {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\n  namespace: %s\n", name, namespace))
	if len(labels) > 0 {
		b.WriteString("  labels:\n")
		for k, v := range labels {
			b.WriteString(fmt.Sprintf("    %s: %q\n", k, v))
		}
	}
	b.WriteString("data:\n")
	for k, v := range data {
		jsonVal, _ := json.Marshal(v)
		b.WriteString(fmt.Sprintf("  %s: %s\n", k, string(jsonVal)))
	}
	return ApplyYAML(b.String(), namespace)
}

// ApplyIngress creates or updates an Ingress for a set of hosts routing to serviceName,
// optionally configuring ingress-nginx external OAuth2 authentication via oauthSetupName.
func ApplyIngress(name, namespace, serviceName string, servicePort int, hosts []string, oauthSetupName string) error {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
`, name, namespace, serviceName))
	if oauthSetupName != "" {
		b.WriteString(fmt.Sprintf("    zero-scale-oauth-setup: %q\n", oauthSetupName))
	}
	b.WriteString(`  annotations:
    nginx.ingress.kubernetes.io/ssl-redirect: "false"
    nginx.ingress.kubernetes.io/proxy-body-size: "50m"
    nginx.ingress.kubernetes.io/upstream-hash-by: "$host"
`)
	if oauthSetupName != "" {
		proxySvc := OAuthProxyServiceName(oauthSetupName)
		b.WriteString(fmt.Sprintf(`    nginx.ingress.kubernetes.io/auth-url: "http://%s.%s.svc.cluster.local:4180/oauth2/auth"
    nginx.ingress.kubernetes.io/auth-signin: "https://$host/oauth2/start?rd=$escaped_request_uri"
    nginx.ingress.kubernetes.io/auth-response-headers: "X-Auth-Request-User,X-Auth-Request-Email,X-Auth-Request-Groups,X-Auth-Request-Preferred-Username"
    nginx.ingress.kubernetes.io/proxy-buffer-size: "16k"
`, proxySvc, namespace))
	}
	b.WriteString("spec:\n")
	if cls := resolveIngressClass(); cls != "" {
		b.WriteString(fmt.Sprintf("  ingressClassName: %s\n", cls))
	}
	b.WriteString("  rules:\n")
	for _, host := range hosts {
		b.WriteString(fmt.Sprintf(`  - host: %s
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: %s
            port:
              number: %d
`, host, serviceName, servicePort))
	}
	return ApplyYAML(b.String(), namespace)
}

// resolveIngressClass returns the desired IngressClass name, or empty string to use cluster default.
// Precedence: $INGRESS_CLASS, then ingress.class in the platform ConfigMap, then "public" (microk8s).
func resolveIngressClass() string {
	if cls := os.Getenv("INGRESS_CLASS"); cls != "" {
		if cls == "none" || cls == "-" || cls == "default" {
			return ""
		}
		return cls
	}
	if p, err := loadPlatform(); err == nil && p != nil && p.Ingress.Class != "" {
		return p.Ingress.Class
	}
	return "public"
}

// ApplyRedirectIngress creates an edge NGINX Ingress redirecting sourceHost to targetHost.
func ApplyRedirectIngress(name, namespace, sourceHost, targetHost, serviceName string) error {
	ingressClassLine := ""
	if cls := resolveIngressClass(); cls != "" {
		ingressClassLine = fmt.Sprintf("  ingressClassName: %s\n", cls)
	}
	manifest := fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
    zero-scale-type: redirect
  annotations:
    nginx.ingress.kubernetes.io/permanent-redirect: "https://%s$request_uri"
spec:
%s  rules:
  - host: %s
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: %s
            port:
              number: 80
`, name, namespace, serviceName, targetHost, ingressClassLine, sourceHost, serviceName)
	return ApplyYAML(manifest, namespace)
}

// ExecPod executes a command inside a pod container with optional input.
func ExecPod(pod, namespace, container string, cmd []string, input string) (string, error) {
	args := []string{"exec"}
	if input != "" {
		args = append(args, "-i")
	}
	args = append(args, pod, "-n", namespace)
	if container != "" {
		args = append(args, "-c", container)
	}
	args = append(args, "--")
	args = append(args, cmd...)
	return RunKubectl(args, input)
}

// CpToPod copies a local file to a pod path.
func CpToPod(localPath, podDestPath, pod, namespace, container string) error {
	dest := fmt.Sprintf("%s/%s:%s", namespace, pod, podDestPath)
	args := []string{"cp", localPath, dest}
	if container != "" {
		args = append(args, "-c", container)
	}
	_, err := RunKubectl(args, "")
	return err
}
