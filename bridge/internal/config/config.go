package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	defaultBindHost     = "127.0.0.1"
	defaultBindPort     = 11434
	defaultUpstreamBase = "http://localhost:1234/v1"
	defaultLogLevel     = "info"
	envBindHost         = "BRIDGE_BIND_HOST"
	envBindPort         = "BRIDGE_BIND_PORT"
	envUpstreamBaseURL  = "LMSTUDIO_BASE_URL"
	envLogLevel         = "BRIDGE_LOG_LEVEL"
)

type Config struct {
	BindHost        string
	BindPort        int
	UpstreamBaseURL string
	LogLevel        string
}

func LoadFromEnv() Config {
	cfg := Config{
		BindHost:        envOrDefault(envBindHost, defaultBindHost),
		BindPort:        defaultBindPort,
		UpstreamBaseURL: normalizeBaseURL(envOrDefault(envUpstreamBaseURL, defaultUpstreamBase)),
		LogLevel:        strings.ToLower(envOrDefault(envLogLevel, defaultLogLevel)),
	}

	if rawPort := strings.TrimSpace(os.Getenv(envBindPort)); rawPort != "" {
		if parsedPort, err := strconv.Atoi(rawPort); err == nil && parsedPort > 0 {
			cfg.BindPort = parsedPort
		}
	}

	return cfg
}

func (c Config) Validate() error {
	return nil
}

func (c Config) Address() string {
	return fmt.Sprintf("%s:%d", c.BindHost, c.BindPort)
}

func normalizeBaseURL(value string) string {
	trimmed := strings.TrimSpace(value)
	return strings.TrimRight(trimmed, "/")
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}
