package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr, DatabaseURL, Provider, APIKey, Model, BaseURL string
	FolderID                                                string
	Workers, MaxAttempts, MaxOutputTokens, MaxQueued        int
	LLMTimeout                                              time.Duration
}

func Load() (Config, error) {
	c := Config{
		HTTPAddr:    env("HTTP_ADDR", "127.0.0.1:8080"),
		DatabaseURL: env("DATABASE_URL", "postgres://supportflow:supportflow_local@127.0.0.1:55433/supportflow?sslmode=disable"),
		Provider:    env("LLM_PROVIDER", "demo"),
	}
	keyName, modelName, baseURLName := "OPENAI_API_KEY", "OPENAI_MODEL", "OPENAI_BASE_URL"
	defaultModel, defaultBaseURL := "gpt-4.1-mini-2025-04-14", "https://api.openai.com/v1"
	switch c.Provider {
	case "yandex":
		keyName, modelName, baseURLName = "YANDEX_API_KEY", "YANDEX_MODEL", "YANDEX_BASE_URL"
		defaultModel, defaultBaseURL = "aliceai-llm-flash", "https://ai.api.cloud.yandex.net/v1"
		c.FolderID = strings.TrimSpace(os.Getenv("YANDEX_FOLDER_ID"))
	case "demo", "openai":
	default:
		return Config{}, errors.New("LLM_PROVIDER must be demo, yandex or openai")
	}
	c.APIKey = strings.TrimSpace(os.Getenv(keyName))
	c.Model = env(modelName, defaultModel)
	c.BaseURL = strings.TrimRight(env(baseURLName, defaultBaseURL), "/")
	for _, setting := range []struct {
		name          string
		target        *int
		fallback, max int
	}{
		{"WORKERS", &c.Workers, 2, 16}, {"MAX_ATTEMPTS", &c.MaxAttempts, 3, 5},
		{"MAX_OUTPUT_TOKENS", &c.MaxOutputTokens, 1500, 8000}, {"MAX_QUEUED_TICKETS", &c.MaxQueued, 100, 10000},
	} {
		value, err := strconv.Atoi(env(setting.name, strconv.Itoa(setting.fallback)))
		if err != nil || value < 1 || value > setting.max {
			return Config{}, fmt.Errorf("%s must be between 1 and %d", setting.name, setting.max)
		}
		*setting.target = value
	}
	var err error
	c.LLMTimeout, err = time.ParseDuration(env("LLM_TIMEOUT", "30s"))
	if err != nil || c.LLMTimeout < time.Second || c.LLMTimeout > 2*time.Minute {
		return Config{}, errors.New("LLM_TIMEOUT must be between 1s and 2m")
	}
	if c.Provider != "demo" && c.APIKey == "" {
		return Config{}, fmt.Errorf("%s is required for %s provider", keyName, c.Provider)
	}
	if c.Provider == "yandex" {
		if c.FolderID == "" {
			return Config{}, errors.New("YANDEX_FOLDER_ID is required for yandex provider")
		}
		if len(c.FolderID) > 128 || !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(c.FolderID) {
			return Config{}, errors.New("invalid YANDEX_FOLDER_ID")
		}
		if len(c.Model) > 128 || !regexp.MustCompile(`^[a-zA-Z0-9._-]+$`).MatchString(c.Model) {
			return Config{}, errors.New("YANDEX_MODEL must be a model name, for example aliceai-llm-flash")
		}
		c.Model = "gpt://" + c.FolderID + "/" + c.Model
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Config{}, fmt.Errorf("invalid %s", baseURLName)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return Config{}, fmt.Errorf("%s requires HTTPS (HTTP is allowed only on loopback)", baseURLName)
	}
	return c, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
