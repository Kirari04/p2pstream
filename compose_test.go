package main

import (
	"os"
	"strings"
	"testing"
)

func TestComposePublicCapacityDefaultsRemainStableReleaseCompatible(t *testing.T) {
	tests := []struct {
		path     string
		expected []string
	}{
		{
			path: "compose.yaml",
			expected: []string{
				`PUBLIC_MAX_CONCURRENT_REQUESTS: "${PUBLIC_MAX_CONCURRENT_REQUESTS:-2048}"`,
				`PUBLIC_MAX_CONNECTIONS_PER_TARGET: "${PUBLIC_MAX_CONNECTIONS_PER_TARGET:-256}"`,
			},
		},
		{
			path: ".env.example",
			expected: []string{
				"# PUBLIC_MAX_CONCURRENT_REQUESTS=2048",
				"# PUBLIC_MAX_CONNECTIONS_PER_TARGET=256",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			contents, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatalf("read %s: %v", test.path, err)
			}
			for _, expected := range test.expected {
				if !strings.Contains(string(contents), expected) {
					t.Errorf("%s is missing stable-release-compatible default %q", test.path, expected)
				}
			}
		})
	}
}
