package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr, DatabaseURL, Provider, APIKey, Model, BaseURL string
	Workers, MaxAttempts, MaxOutputTokens, MaxQueued        int
	LLMTimeout                                              time.Duration
}

func Load() (Config, error) {
	c := Config{
		HTTPAddr:    env("HTTP_ADDR", "127.0.0.1:8080"),
		DatabaseURL: env("DATABASE_URL", "postgres://supportflow:supportflow_local@127.0.0.1:55433/supportflow?sslmode=disable"),
		Provider:    env("LLM_PROVIDER", "demo"), APIKey: os.Getenv("OPENAI_API_KEY"),
		Model:   env("OPENAI_MODEL", "gpt-4.1-mini-2025-04-14"),
		BaseURL: strings.TrimRight(env("OPENAI_BASE_URL", "https://api.openai.com/v1"), "/"),
	}
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
	if c.Provider != "demo" && c.Provider != "openai" {
		return Config{}, errors.New("LLM_PROVIDER must be demo or openai")
	}
	if c.Provider == "openai" && strings.TrimSpace(c.APIKey) == "" {
		return Config{}, errors.New("OPENAI_API_KEY is required for openai provider")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Config{}, errors.New("invalid OPENAI_BASE_URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return Config{}, errors.New("OPENAI_BASE_URL requires HTTPS (HTTP is allowed only on loopback)")
	}
	return c, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
