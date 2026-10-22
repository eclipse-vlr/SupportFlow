package config

import "testing"

func TestConfigurationRejectsUnsafeSettings(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"WORKERS", "0"}, {"MAX_ATTEMPTS", "999"}, {"LLM_TIMEOUT", "0s"},
		{"LLM_PROVIDER", "unknown"}, {"OPENAI_BASE_URL", "http://example.com/v1"},
		{"OPENAI_BASE_URL", "https://user:password@example.com"}, {"OPENAI_BASE_URL", "https://example.com?key=secret"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("expected invalid configuration")
			}
		})
	}
	t.Run("missing key", func(t *testing.T) {
		t.Setenv("LLM_PROVIDER", "openai")
		t.Setenv("OPENAI_API_KEY", "")
		if _, err := Load(); err == nil {
			t.Fatal("expected missing key")
		}
	})
}
