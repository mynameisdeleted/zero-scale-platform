package jwtauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"sync"
	"time"
)

var (
	globalSigner *Signer
	signerOnce   sync.Once
)

// Signer handles minting and signing ephemeral ES256 JWTs using an in-memory ECDSA P-256 keypair.
type Signer struct {
	privateKey *ecdsa.PrivateKey
	publicKey  *ecdsa.PublicKey
	publicPEM  string
	mu         sync.RWMutex
}

// GetSigner returns the singleton gateway JWT signer.
func GetSigner() *Signer {
	signerOnce.Do(func() {
		s, err := newSigner()
		if err != nil {
			log.Fatalf("Failed to initialize gateway JWT signer: %v", err)
		}
		globalSigner = s
	})
	return globalSigner
}

func newSigner() (*Signer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed generating ECDSA P-256 key: %w", err)
	}

	pubBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed marshaling public key: %w", err)
	}

	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	})

	return &Signer{
		privateKey: key,
		publicKey:  &key.PublicKey,
		publicPEM:  string(pubPEM),
	}, nil
}

// InitializeWithPEM loads existing PEM keys (e.g. from Kubernetes Secret) or generates a new pair.
func (s *Signer) InitializeWithPEM(privPEM, pubPEM string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if privPEM != "" {
		block, _ := pem.Decode([]byte(privPEM))
		if block != nil {
			if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
				if ecKey, ok := key.(*ecdsa.PrivateKey); ok {
					s.privateKey = ecKey
					s.publicKey = &ecKey.PublicKey
					s.publicPEM = pubPEM
					return nil
				}
			} else if ecKey, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
				s.privateKey = ecKey
				s.publicKey = &ecKey.PublicKey
				s.publicPEM = pubPEM
				return nil
			}
		}
	}
	return nil
}

// PublicPEM returns the standard PEM-encoded public key string for injection into apps.
func (s *Signer) PublicPEM() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.publicPEM
}

// Claims represents the standard and custom JWT claims passed downstream.
type Claims struct {
	Issuer            string   `json:"iss"`
	Subject           string   `json:"sub"`
	Email             string   `json:"email"`
	PreferredUsername string   `json:"preferred_username,omitempty"`
	Groups            []string `json:"groups,omitempty"`
	Host              string   `json:"host"`
	IssuedAt          int64    `json:"iat"`
	ExpiresAt         int64    `json:"exp"`
}

// MintToken creates and signs a 60-second ES256 JWT for the authenticated user and target host.
func (s *Signer) MintToken(email, user string, groups []string, host string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	sub := email
	if sub == "" {
		sub = user
	}

	claims := Claims{
		Issuer:            "zero-scale-gateway",
		Subject:           sub,
		Email:             email,
		PreferredUsername: user,
		Groups:            groups,
		Host:              host,
		IssuedAt:          now.Unix(),
		ExpiresAt:         now.Add(60 * time.Second).Unix(), // Ephemeral 60s window
	}

	headerJSON := `{"alg":"ES256","typ":"JWT"}`
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	b64Header := base64.RawURLEncoding.EncodeToString([]byte(headerJSON))
	b64Payload := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := b64Header + "." + b64Payload

	hash := sha256.Sum256([]byte(signingInput))
	r, sBig, err := ecdsa.Sign(rand.Reader, s.privateKey, hash[:])
	if err != nil {
		return "", fmt.Errorf("ECDSA signing failed: %w", err)
	}

	// IEEE P1363 standard 64-byte signature for ES256 (32 bytes r + 32 bytes s)
	sigBytes := make([]byte, 64)
	rBytes := r.Bytes()
	sBytes := sBig.Bytes()
	copy(sigBytes[32-len(rBytes):32], rBytes)
	copy(sigBytes[64-len(sBytes):64], sBytes)

	b64Sig := base64.RawURLEncoding.EncodeToString(sigBytes)
	return signingInput + "." + b64Sig, nil
}
