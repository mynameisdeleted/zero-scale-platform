package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// JWKS represents the JSON Web Key Set standard format (RFC 7517)
type JWKS struct {
	Keys []map[string]interface{} `json:"keys"`
}

type Aggregator struct {
	giteaURL     string
	k8sURL       string
	k8sClient    *http.Client
	standardHTTP *http.Client
	mu           sync.RWMutex
	cachedKeys   []byte
	lastUpdated  time.Time
}

func newAggregator(giteaURL, k8sURL string) *Aggregator {
	// Standard client for Gitea
	standardClient := &http.Client{
		Timeout: 10 * time.Second,
	}

	// Kubernetes API client using in-cluster CA certificate
	k8sTransport := &http.Transport{
		TLSClientConfig: &tls.Config{},
	}
	caCertPath := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	if caData, err := os.ReadFile(caCertPath); err == nil {
		certPool := x509.NewCertPool()
		if certPool.AppendCertsFromPEM(caData) {
			k8sTransport.TLSClientConfig.RootCAs = certPool
		}
	} else {
		// Fallback for local testing / self-signed
		k8sTransport.TLSClientConfig.InsecureSkipVerify = true
	}

	k8sClient := &http.Client{
		Transport: k8sTransport,
		Timeout:   10 * time.Second,
	}

	return &Aggregator{
		giteaURL:     giteaURL,
		k8sURL:       k8sURL,
		k8sClient:    k8sClient,
		standardHTTP: standardClient,
	}
}

func fetchKeys(client *http.Client, url string, bearerToken string) ([]map[string]interface{}, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d from %s: %s", resp.StatusCode, url, string(body))
	}

	var jwks JWKS
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return nil, err
	}
	return jwks.Keys, nil
}

func (a *Aggregator) refresh() error {
	var combinedKeys []map[string]interface{}

	// 1. Fetch Gitea keys
	if giteaKeys, err := fetchKeys(a.standardHTTP, a.giteaURL, ""); err != nil {
		log.Printf("[WARN] Failed to fetch Gitea JWKS from %s: %v", a.giteaURL, err)
	} else {
		combinedKeys = append(combinedKeys, giteaKeys...)
	}

	// 2. Fetch Kubernetes API server keys
	var k8sToken string
	tokenPath := "/var/run/secrets/kubernetes.io/serviceaccount/token"
	if tokenData, err := os.ReadFile(tokenPath); err == nil {
		k8sToken = string(tokenData)
	}
	if k8sKeys, err := fetchKeys(a.k8sClient, a.k8sURL, k8sToken); err != nil {
		log.Printf("[WARN] Failed to fetch Kubernetes JWKS from %s: %v", a.k8sURL, err)
	} else {
		combinedKeys = append(combinedKeys, k8sKeys...)
	}

	if len(combinedKeys) == 0 {
		return fmt.Errorf("unable to fetch keys from any provider")
	}

	marshaled, err := json.Marshal(JWKS{Keys: combinedKeys})
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.cachedKeys = marshaled
	a.lastUpdated = time.Now()
	a.mu.Unlock()

	log.Printf("[INFO] Successfully refreshed JWKS cache with %d total keys", len(combinedKeys))
	return nil
}

func (a *Aggregator) startRefreshLoop(interval time.Duration) {
	if err := a.refresh(); err != nil {
		log.Printf("[ERROR] Initial JWKS refresh failed: %v", err)
	}
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			if err := a.refresh(); err != nil {
				log.Printf("[ERROR] Background JWKS refresh failed: %v", err)
			}
		}
	}()
}

func (a *Aggregator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/keys":
		a.mu.RLock()
		data := a.cachedKeys
		a.mu.RUnlock()

		if len(data) == 0 {
			http.Error(w, "keys not yet loaded", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Write(data)

	case "/healthz":
		a.mu.RLock()
		hasKeys := len(a.cachedKeys) > 0
		a.mu.RUnlock()

		if !hasKeys {
			http.Error(w, "warming up", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))

	default:
		http.NotFound(w, r)
	}
}

func main() {
	giteaURL := os.Getenv("GITEA_JWKS_URL")
	if giteaURL == "" {
		giteaURL = "https://git.fairfaxmedia.net/login/oauth/keys"
	}

	k8sURL := os.Getenv("K8S_JWKS_URL")
	if k8sURL == "" {
		k8sURL = "https://kubernetes.default.svc/openid/v1/jwks"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	aggregator := newAggregator(giteaURL, k8sURL)
	aggregator.startRefreshLoop(1 * time.Minute)

	log.Printf("[INFO] Temporal JWKS Aggregator listening on :%s", port)
	if err := http.ListenAndServe(":"+port, aggregator); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
