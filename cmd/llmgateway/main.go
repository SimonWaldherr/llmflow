package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/SimonWaldherr/llmflow/llmgateway"
)

var version = "dev"

func main() {
	var (
		cfgPath   string
		logLevel  string
		showVer   bool
		openAIAdr string
		ollamaAdr string
		adminAdr  string
	)

	fs := flag.NewFlagSet("llmgateway", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfgPath, "config", "examples/llmgateway.yaml", "path to gateway yaml config")
	fs.StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")
	fs.BoolVar(&showVer, "version", false, "print version and exit")
	fs.StringVar(&openAIAdr, "openai-addr", "", "override OpenAI listen address")
	fs.StringVar(&ollamaAdr, "ollama-addr", "", "override Ollama listen address")
	fs.StringVar(&adminAdr, "admin-addr", "", "override admin/control-plane listen address")

	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, "usage: llmgateway --config path/to/llmgateway.yaml")
		os.Exit(2)
	}
	if showVer {
		fmt.Println("llmgateway", version)
		return
	}

	cfg, err := llmgateway.LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid config:", err)
		os.Exit(1)
	}
	if openAIAdr != "" {
		cfg.Servers.OpenAIAddr = openAIAdr
	}
	if ollamaAdr != "" {
		cfg.Servers.OllamaAddr = ollamaAdr
	}
	if adminAdr != "" {
		cfg.Admin.Addr = adminAdr
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLogLevel(logLevel)}))
	gw, err := llmgateway.NewGateway(cfg, logger)
	if err != nil {
		logger.Error("create gateway", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := gw.Run(ctx); err != nil {
		logger.Error("gateway failed", "error", err)
		os.Exit(1)
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
