package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"lmstudio-copilot-bridge/internal/config"
	"lmstudio-copilot-bridge/internal/server"
)

var version = "0.17.7"

func main() {
	cfg := config.LoadFromEnv()

	flag.StringVar(&cfg.BindHost, "host", cfg.BindHost, "bind host")
	flag.IntVar(&cfg.BindPort, "port", cfg.BindPort, "bind port")
	flag.StringVar(&cfg.UpstreamBaseURL, "upstream", cfg.UpstreamBaseURL, "LM Studio OpenAI-compatible base URL")
	flag.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level: debug, info, warn, error")
	flag.Parse()

	if err := cfg.Validate(); err != nil {
		slog.New(slog.NewJSONHandler(os.Stdout, nil)).Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)}))
	handler := server.NewHandler(cfg, logger, version)

	logger.Info("starting bridge",
		"address", cfg.Address(),
		"upstream_base_url", cfg.UpstreamBaseURL,
		"version", version,
	)

	server := &http.Server{
		Addr:              cfg.Address(),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func parseLogLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}