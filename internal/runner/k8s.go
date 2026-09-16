package runner

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

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
