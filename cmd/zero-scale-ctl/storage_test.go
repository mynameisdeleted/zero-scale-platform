package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateBinaryArchitecture(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name        string
		content     []byte
		envVar      string
		expectError bool
		errorSubstr string
	}{
		{
			name:        "Shell script with shebang",
			content:     []byte("#!/bin/bash\necho 'hello world'\n"),
			expectError: false,
		},
		{
			name:        "WebAssembly binary",
			content:     []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00},
			expectError: false,
		},
		{
			name:        "macOS Mach-O 64-bit Little Endian",
			content:     []byte{0xcf, 0xfa, 0xed, 0xfe, 0x07, 0x00, 0x00, 0x01},
			expectError: true,
			errorSubstr: "macOS (Mach-O) binary",
		},
		{
			name:        "macOS Mach-O Fat Universal",
			content:     []byte{0xca, 0xfe, 0xba, 0xbe, 0x00, 0x00, 0x00, 0x02},
			expectError: true,
			errorSubstr: "macOS (Mach-O) binary",
		},
		{
			name:        "Windows PE Executable",
			content:     []byte{'M', 'Z', 0x90, 0x00, 0x03, 0x00, 0x00, 0x00},
			expectError: true,
			errorSubstr: "Windows (PE) executable",
		},
		{
			name: "Linux ELF ARM64",
			content: func() []byte {
				// Minimal ELF64 header for ARM64 (e_machine = 0xB7 = 183)
				b := make([]byte, 64)
				copy(b[0:4], []byte{0x7f, 'E', 'L', 'F'})
				b[4] = 2 // 64-bit
				b[5] = 1 // Little-endian
				b[6] = 1 // Version 1
				b[18] = 0xB7 // e_machine = 183 (EM_AARCH64)
				return b
			}(),
			expectError: true,
			errorSubstr: "Linux ARM64",
		},
		{
			name: "macOS Mach-O bypassed via env var",
			content: []byte{0xcf, 0xfa, 0xed, 0xfe, 0x07, 0x00, 0x00, 0x01},
			envVar: "1",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filePath := filepath.Join(tempDir, "test_bin_"+filepath.Base(t.Name()))
			if err := os.WriteFile(filePath, tt.content, 0755); err != nil {
				t.Fatalf("failed to write test file: %v", err)
			}

			if tt.envVar != "" {
				os.Setenv("ZERO_SCALE_SKIP_ARCH_CHECK", tt.envVar)
				defer os.Unsetenv("ZERO_SCALE_SKIP_ARCH_CHECK")
			} else {
				os.Unsetenv("ZERO_SCALE_SKIP_ARCH_CHECK")
			}

			err := ValidateBinaryArchitecture(filePath)
			if tt.expectError {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errorSubstr)
				}
				if tt.errorSubstr != "" && !contains(err.Error(), tt.errorSubstr) {
					t.Fatalf("expected error containing %q, got: %v", tt.errorSubstr, err)
				}
			} else {
				if err != nil {
					t.Fatalf("expected no error, got: %v", err)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return filepath.Clean(s) != "" && filepath.Clean(substr) != "" && len(s) >= len(substr) && (s == substr || len(s) > 0 && (stringContains(s, substr)))
}

func stringContains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
