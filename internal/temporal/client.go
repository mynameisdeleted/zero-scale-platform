package temporal

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	defaultK8sTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
)

// K8sTokenHeadersProvider implements Temporal client.HeadersProvider to supply
// the cryptographically signed Kubernetes Projected ServiceAccount token
// to Temporal Server frontend gRPC calls.
type K8sTokenHeadersProvider struct {
	tokenPath string
	mu        sync.RWMutex
	cached    string
	lastRead  time.Time
	cacheTTL  time.Duration
}

// NewK8sTokenHeadersProvider creates a new provider. If customPath is empty,
// it uses the standard in-cluster path.
func NewK8sTokenHeadersProvider(customPath string) *K8sTokenHeadersProvider {
	path := defaultK8sTokenPath
	if customPath != "" {
		path = customPath
	}
	return &K8sTokenHeadersProvider{
		tokenPath: path,
		cacheTTL:  1 * time.Minute,
	}
}

// GetHeaders returns the authorization header containing the Bearer token.
// Temporal SDK calls this method on gRPC requests.
func (p *K8sTokenHeadersProvider) GetHeaders(ctx context.Context) (map[string]string, error) {
	token, err := p.getToken()
	if err != nil {
		// Fallback for local debugging outside of Kubernetes cluster
		if envToken := os.Getenv("TEMPORAL_AUTH_TOKEN"); envToken != "" {
			return map[string]string{
				"authorization": "Bearer " + strings.TrimSpace(envToken),
			}, nil
		}
		return nil, fmt.Errorf("temporal auth: unable to obtain token: %w", err)
	}

	return map[string]string{
		"authorization": "Bearer " + token,
	}, nil
}

func (p *K8sTokenHeadersProvider) getToken() (string, error) {
	p.mu.RLock()
	if p.cached != "" && time.Since(p.lastRead) < p.cacheTTL {
		token := p.cached
		p.mu.RUnlock()
		return token, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock
	if p.cached != "" && time.Since(p.lastRead) < p.cacheTTL {
		return p.cached, nil
	}

	data, err := os.ReadFile(p.tokenPath)
	if err != nil {
		return "", err
	}

	p.cached = strings.TrimSpace(string(data))
	p.lastRead = time.Now()
	return p.cached, nil
}
