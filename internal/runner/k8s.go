package runner

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	tokenCache   = make(map[string]cacheEntry)
	tokenCacheMu sync.Mutex
)

type cacheEntry struct {
	allowed bool
	reason  string
	expiry  time.Time
}

// FetchK8sResource dynamically retrieves a ConfigMap or Secret from the in-cluster Kubernetes API
func FetchK8sResource(resourceType, name, namespace string) (map[string]string, error) {
	tokenBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(string(tokenBytes))

	k8sHost := os.Getenv("KUBERNETES_SERVICE_HOST")
	if k8sHost == "" {
		k8sHost = "kubernetes.default.svc"
	}
	k8sPort := os.Getenv("KUBERNETES_SERVICE_PORT")
	if k8sPort == "" {
		k8sPort = "443"
	}
	if namespace == "" {
		namespace = "default"
	}

	reqURL := fmt.Sprintf("https://%s:%s/api/v1/namespaces/%s/%s/%s", k8sHost, k8sPort, namespace, resourceType, name)
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("k8s API returned status %d for %s/%s", resp.StatusCode, resourceType, name)
	}

	var res struct {
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	if resourceType == "secrets" {
		decoded := make(map[string]string)
		for k, v := range res.Data {
			b, err := base64.StdEncoding.DecodeString(v)
			if err == nil {
				decoded[k] = string(b)
			} else {
				decoded[k] = v
			}
		}
		return decoded, nil
	}

	return res.Data, nil
}

// VerifyKubeToken checks if the provided Bearer token is a valid Kubernetes token
// with permission to read pod logs in the specified namespace via SelfSubjectAccessReview.
func VerifyKubeToken(token, namespace string) (bool, string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return false, "Missing token", nil
	}

	tokenCacheMu.Lock()
	if entry, ok := tokenCache[token]; ok && time.Now().Before(entry.expiry) {
		tokenCacheMu.Unlock()
		return entry.allowed, entry.reason, nil
	}
	tokenCacheMu.Unlock()

	k8sHost := os.Getenv("KUBERNETES_SERVICE_HOST")
	if k8sHost == "" {
		k8sHost = "kubernetes.default.svc"
	}
	k8sPort := os.Getenv("KUBERNETES_SERVICE_PORT")
	if k8sPort == "" {
		k8sPort = "443"
	}
	if namespace == "" {
		namespace = "default"
	}

	reqBody := fmt.Sprintf(`{
		"apiVersion": "authorization.k8s.io/v1",
		"kind": "SelfSubjectAccessReview",
		"spec": {
			"resourceAttributes": {
				"namespace": "%s",
				"verb": "get",
				"resource": "pods",
				"subresource": "log"
			}
		}
	}`, namespace)

	reqURL := fmt.Sprintf("https://%s:%s/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", k8sHost, k8sPort)
	req, err := http.NewRequest("POST", reqURL, strings.NewReader(reqBody))
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return false, "Invalid or expired Kubernetes token", nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return false, fmt.Sprintf("Kubernetes API returned status %d", resp.StatusCode), nil
	}

	var res struct {
		Status struct {
			Allowed bool   `json:"allowed"`
			Reason  string `json:"reason"`
		} `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return false, "", err
	}

	// Cache successful or denied validation for 2 minutes
	tokenCacheMu.Lock()
	tokenCache[token] = cacheEntry{
		allowed: res.Status.Allowed,
		reason:  res.Status.Reason,
		expiry:  time.Now().Add(2 * time.Minute),
	}
	tokenCacheMu.Unlock()

	return res.Status.Allowed, res.Status.Reason, nil
}

// ConfigMapItem is one ConfigMap returned by ListK8sConfigMaps.
type ConfigMapItem struct {
	Name string
	Data map[string]string
}

// ListK8sConfigMaps lists ConfigMaps in namespace matching labelSelector via the in-cluster API.
func ListK8sConfigMaps(namespace, labelSelector string) ([]ConfigMapItem, error) {
	tokenBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return nil, err
	}
	k8sHost := os.Getenv("KUBERNETES_SERVICE_HOST")
	if k8sHost == "" {
		k8sHost = "kubernetes.default.svc"
	}
	k8sPort := os.Getenv("KUBERNETES_SERVICE_PORT")
	if k8sPort == "" {
		k8sPort = "443"
	}
	if namespace == "" {
		namespace = "default"
	}

	reqURL := fmt.Sprintf("https://%s:%s/api/v1/namespaces/%s/configmaps?labelSelector=%s",
		k8sHost, k8sPort, namespace, url.QueryEscape(labelSelector))
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tokenBytes)))

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("k8s API returned status %d listing configmaps", resp.StatusCode)
	}

	var res struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Data map[string]string `json:"data"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	items := make([]ConfigMapItem, 0, len(res.Items))
	for _, it := range res.Items {
		items = append(items, ConfigMapItem{Name: it.Metadata.Name, Data: it.Data})
	}
	return items, nil
}
