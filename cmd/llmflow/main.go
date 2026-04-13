package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/SimonWaldherr/llmflow/internal/app"
	"github.com/SimonWaldherr/llmflow/internal/config"
	"github.com/SimonWaldherr/llmflow/internal/web"
)

// version is set via -ldflags "-X main.version=<tag>" at build time.
var version = "dev"

const usageLine = "usage: llmflow [run|validate|web] --config path/to/config.yaml"

type cliOptions struct {
	cfgPath  string
	logLevel string
	dryRun   bool
	showVer  bool
	webAddr  string
}

func main() {
	opts, cmd, err := parseCLIArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageLine)
		os.Exit(2)
	}

	if opts.showVer {
		fmt.Println("llmflow", version)
		return
	}

	if cmd == "" {
		fmt.Fprintln(os.Stderr, usageLine)
		os.Exit(2)
	}

	level := parseLogLevel(opts.logLevel)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	switch cmd {
	case "web":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		srv := web.NewServer(opts.webAddr, logger)
		if err := srv.Run(ctx); err != nil {
			logger.Error("web server failed", "error", err)
			os.Exit(1)
		}
		return
	default:
	}

	cfg, err := config.Load(opts.cfgPath)
	if err != nil {
		logger.Error("invalid config", "error", err)
		os.Exit(1)
	}

	switch cmd {
	case "validate":
		logger.Info("configuration valid", "config", opts.cfgPath)
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		a := app.New(cfg, logger).WithDryRun(opts.dryRun)
		if err := a.Run(ctx); err != nil {
			logger.Error("run failed", "error", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		os.Exit(2)
	}
}

func parseCLIArgs(args []string) (cliOptions, string, error) {
	opts := cliOptions{
		cfgPath:  "config.yaml",
		logLevel: "info",
		webAddr:  ":8080",
	}

	fs := flag.NewFlagSet("llmflow", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.cfgPath, "config", opts.cfgPath, "path to YAML/JSON configuration file")
	fs.StringVar(&opts.logLevel, "log-level", opts.logLevel, "log level: debug, info, warn, error")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "skip LLM calls and write placeholder responses")
	fs.BoolVar(&opts.showVer, "version", false, "print version and exit")
	fs.StringVar(&opts.webAddr, "addr", opts.webAddr, "listen address for the web UI (used with 'web' command)")

	cmd := ""
	flagArgs := args
	if cmdIdx := findCommandIndex(args); cmdIdx >= 0 {
		cmd = args[cmdIdx]
		flagArgs = make([]string, 0, len(args)-1)
		flagArgs = append(flagArgs, args[:cmdIdx]...)
		flagArgs = append(flagArgs, args[cmdIdx+1:]...)
	}

	if err := fs.Parse(flagArgs); err != nil {
		return opts, "", err
	}
	if cmd == "" && fs.NArg() > 0 {
		cmd = fs.Arg(0)
	}

	return opts, cmd, nil
}

func findCommandIndex(args []string) int {
	expectValue := false
	for i, arg := range args {
		if expectValue {
			expectValue = false
			continue
		}
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "-") {
			flagName, hasInlineValue := parseFlagToken(arg)
			if !hasInlineValue && flagExpectsValue(flagName) {
				expectValue = true
			}
			continue
		}
		if isCommand(arg) {
			return i
		}
	}
	return -1
}

func parseFlagToken(arg string) (name string, hasInlineValue bool) {
	trimmed := strings.TrimLeft(arg, "-")
	if trimmed == "" {
		return "", false
	}
	if idx := strings.IndexByte(trimmed, '='); idx >= 0 {
		return trimmed[:idx], true
	}
	return trimmed, false
}

func flagExpectsValue(name string) bool {
	switch name {
	case "config", "log-level", "addr":
		return true
	default:
		return false
	}
}

func isCommand(s string) bool {
	switch s {
	case "run", "validate", "web":
		return true
	default:
		return false
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
