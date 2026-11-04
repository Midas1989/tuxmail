// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import "testing"

// AUDIT-C: background categorization only uses local/LAN AI providers;
// the check must look at the host, not at substrings of the URL.
func TestIsLocalAI(t *testing.T) {
	for _, c := range []struct {
		provider, url string
		want          bool
	}{
		{"ollama", "", true},
		{"ollama", "http://localhost:11434", true},
		{"custom", "http://192.168.1.5:8080/v1", true},
		{"custom", "http://10.0.0.2/v1", true},
		{"custom", "http://[::1]:1234", true},
		{"ollama", "https://ollama.cloud.example", false},
		{"custom", "https://10.evil.example/v1", false},
		{"custom", "https://127.evil.example/v1", false},
		{"custom", "https://api.example/?x=://localhost", false},
		{"openai", "", false},
	} {
		if got := isLocalAI(AiSettings{Provider: c.provider, BaseURL: c.url}); got != c.want {
			t.Errorf("isLocalAI(%s, %s) = %v, want %v", c.provider, c.url, got, c.want)
		}
	}
}
