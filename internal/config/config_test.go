package config

import (
	"strings"
	"testing"
)

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

func TestYandexUsesSeparateCredentials(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "yandex")
	t.Setenv("OPENAI_API_KEY", "openai-secret-must-stay-with-openai")
	t.Setenv("OPENAI_MODEL", "openai-model")
	t.Setenv("OPENAI_BASE_URL", "https://api.openai.com/v1")
	t.Setenv("YANDEX_API_KEY", "yandex-test-secret")
	t.Setenv("YANDEX_FOLDER_ID", "test-folder")
	t.Setenv("YANDEX_MODEL", "")
	t.Setenv("YANDEX_BASE_URL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "yandex-test-secret" || cfg.Model != "gpt://test-folder/aliceai-llm-flash" || cfg.BaseURL != "https://ai.api.cloud.yandex.net/v1" || cfg.FolderID != "test-folder" {
		t.Fatal("Yandex must use its own key, folder, model URI and endpoint")
	}

	t.Setenv("YANDEX_API_KEY", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "YANDEX_API_KEY") {
		t.Fatal("missing Yandex key must not fall back to the OpenAI key")
	}
}

func TestYandexEndpointValidation(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "yandex")
	t.Setenv("YANDEX_API_KEY", "yandex-test-secret")
	t.Setenv("YANDEX_FOLDER_ID", "test-folder")
	for _, endpoint := range []string{"http://example.com", "https://user:secret@example.com", "https://ai.api.cloud.yandex.net/v1?key=secret"} {
		t.Run(endpoint, func(t *testing.T) {
			t.Setenv("YANDEX_BASE_URL", endpoint)
			if _, err := Load(); err == nil {
				t.Fatal("unsafe endpoint accepted")
			}
		})
	}
}

func TestYandexRequiresFolderAndModelName(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "yandex")
	t.Setenv("YANDEX_API_KEY", "yandex-test-secret")
	t.Setenv("YANDEX_FOLDER_ID", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "YANDEX_FOLDER_ID") {
		t.Fatal("missing folder accepted")
	}
	t.Setenv("YANDEX_FOLDER_ID", "folder\r\nx-injected: value")
	if _, err := Load(); err == nil {
		t.Fatal("invalid folder accepted")
	}
	t.Setenv("YANDEX_FOLDER_ID", "test-folder")
	t.Setenv("YANDEX_MODEL", "gpt://different-folder/aliceai-llm-flash")
	if _, err := Load(); err == nil {
		t.Fatal("model from another folder accepted")
	}
}
