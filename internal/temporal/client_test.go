package temporal

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestK8sTokenHeadersProvider_File(t *testing.T) {
	tempDir := t.TempDir()
	tokenFile := filepath.Join(tempDir, "token")
	expectedToken := "test-k8s-jwt-token-12345"

	if err := os.WriteFile(tokenFile, []byte(expectedToken+"\n"), 0600); err != nil {
		t.Fatalf("failed to write test token file: %v", err)
	}

	provider := NewK8sTokenHeadersProvider(tokenFile)
	headers, err := provider.GetHeaders(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	authHeader, ok := headers["authorization"]
	if !ok {
		t.Fatalf("missing authorization header")
	}

	expectedHeader := "Bearer " + expectedToken
	if authHeader != expectedHeader {
		t.Errorf("got %q, want %q", authHeader, expectedHeader)
	}
}

func TestK8sTokenHeadersProvider_EnvFallback(t *testing.T) {
	tempDir := t.TempDir()
	missingFile := filepath.Join(tempDir, "nonexistent")

	expectedToken := "env-jwt-token-999"
	t.Setenv("TEMPORAL_AUTH_TOKEN", expectedToken)

	provider := NewK8sTokenHeadersProvider(missingFile)
	headers, err := provider.GetHeaders(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	authHeader := headers["authorization"]
	expectedHeader := "Bearer " + expectedToken
	if authHeader != expectedHeader {
		t.Errorf("got %q, want %q", authHeader, expectedHeader)
	}
}
