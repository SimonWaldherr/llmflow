package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/SimonWaldherr/llmflow/internal/app"
	"github.com/SimonWaldherr/llmflow/internal/config"
	"github.com/SimonWaldherr/llmflow/internal/web"
)

// version is set via -ldflags "-X main.version=<tag>" at build time.
var version = "dev"

func main() {
	var (
		cfgPath  string
		logLevel string
		dryRun   bool
		showVer  bool
		webAddr  string
	)
	flag.StringVar(&cfgPath, "config", "config.yaml", "path to YAML/JSON configuration file")
	flag.StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")
	flag.BoolVar(&dryRun, "dry-run", false, "skip LLM calls and write placeholder responses")
	flag.BoolVar(&showVer, "version", false, "print version and exit")
	flag.StringVar(&webAddr, "addr", ":8080", "listen address for the web UI (used with 'web' command)")
	flag.Parse()

	if showVer {
		fmt.Println("llmflow", version)
		return
	}

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: llmflow [run|validate|web] --config path/to/config.yaml")
		os.Exit(2)
	}
	cmd := flag.Arg(0)

	level := parseLogLevel(logLevel)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	switch cmd {
	case "web":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		srv := web.NewServer(webAddr, logger)
		if err := srv.Run(ctx); err != nil {
			logger.Error("web server failed", "error", err)
			os.Exit(1)
		}
		return
	default:
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Error("invalid config", "error", err)
		os.Exit(1)
	}

	switch cmd {
	case "validate":
		logger.Info("configuration valid", "config", cfgPath)
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		a := app.New(cfg, logger).WithDryRun(dryRun)
		if err := a.Run(ctx); err != nil {
			logger.Error("run failed", "error", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		os.Exit(2)
	}
}

func parseLogLevel(s string) slog.Level {
	switch s {
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
