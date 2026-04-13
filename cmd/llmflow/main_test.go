package main

import "testing"

func TestParseCLIArgsCommandFirst(t *testing.T) {
	opts, cmd, err := parseCLIArgs([]string{"web", "--addr", ":8082"})
	if err != nil {
		t.Fatalf("parseCLIArgs returned error: %v", err)
	}
	if cmd != "web" {
		t.Fatalf("cmd = %q, want web", cmd)
	}
	if opts.webAddr != ":8082" {
		t.Fatalf("webAddr = %q, want :8082", opts.webAddr)
	}
}

func TestParseCLIArgsFlagsBeforeCommand(t *testing.T) {
	opts, cmd, err := parseCLIArgs([]string{"--config", "examples/config.yaml", "run", "--dry-run"})
	if err != nil {
		t.Fatalf("parseCLIArgs returned error: %v", err)
	}
	if cmd != "run" {
		t.Fatalf("cmd = %q, want run", cmd)
	}
	if opts.cfgPath != "examples/config.yaml" {
		t.Fatalf("cfgPath = %q, want examples/config.yaml", opts.cfgPath)
	}
	if !opts.dryRun {
		t.Fatal("dryRun should be true")
	}
}

func TestParseCLIArgsCommandNotTakenFromFlagValue(t *testing.T) {
	opts, cmd, err := parseCLIArgs([]string{"--config", "web", "validate"})
	if err != nil {
		t.Fatalf("parseCLIArgs returned error: %v", err)
	}
	if cmd != "validate" {
		t.Fatalf("cmd = %q, want validate", cmd)
	}
	if opts.cfgPath != "web" {
		t.Fatalf("cfgPath = %q, want web", opts.cfgPath)
	}
}

func TestParseCLIArgsVersionWithoutCommand(t *testing.T) {
	opts, cmd, err := parseCLIArgs([]string{"--version"})
	if err != nil {
		t.Fatalf("parseCLIArgs returned error: %v", err)
	}
	if cmd != "" {
		t.Fatalf("cmd = %q, want empty", cmd)
	}
	if !opts.showVer {
		t.Fatal("showVer should be true")
	}
}

func TestParseCLIArgsUnknownCommand(t *testing.T) {
	_, cmd, err := parseCLIArgs([]string{"unknown", "--addr", ":8082"})
	if err != nil {
		t.Fatalf("parseCLIArgs returned error: %v", err)
	}
	if cmd != "unknown" {
		t.Fatalf("cmd = %q, want unknown", cmd)
	}
}
