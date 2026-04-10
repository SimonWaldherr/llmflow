package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	API        APIConfig        `json:"api" yaml:"api"`
	Prompt     PromptConfig     `json:"prompt" yaml:"prompt"`
	Input      InputConfig      `json:"input" yaml:"input"`
	Output     OutputConfig     `json:"output" yaml:"output"`
	Processing ProcessingConfig `json:"processing" yaml:"processing"`
}

// Provider constants for well-known LLM providers.
const (
	ProviderOpenAI    = "openai"
	ProviderGemini    = "gemini"
	ProviderOllama    = "ollama"
	ProviderLMStudio  = "lmstudio"
	ProviderAnthropic = "anthropic"
	ProviderGeneric   = "generic" // OpenAI-compatible generic endpoint
)

type APIConfig struct {
	// Provider selects the API dialect. One of: openai, gemini, ollama, lmstudio, anthropic, generic.
	// Defaults to "openai" when empty.
	Provider string `json:"provider" yaml:"provider"`
	BaseURL  string `json:"base_url" yaml:"base_url"`
	// APIKeyEnv is the name of the environment variable that holds the API key.
	APIKeyEnv       string        `json:"api_key_env" yaml:"api_key_env"`
	Model           string        `json:"model" yaml:"model"`
	Timeout         time.Duration `json:"timeout" yaml:"timeout"`
	MaxOutputTokens int64         `json:"max_output_tokens" yaml:"max_output_tokens"`
	RateLimitRPM    int           `json:"rate_limit_rpm" yaml:"rate_limit_rpm"`
}

type PromptConfig struct {
	System        string `json:"system" yaml:"system"`
	PrePrompt     string `json:"pre_prompt" yaml:"pre_prompt"`
	InputTemplate string `json:"input_template" yaml:"input_template"`
	PostPrompt    string `json:"post_prompt" yaml:"post_prompt"`
}

type InputConfig struct {
	Type   string        `json:"type" yaml:"type"`
	Path   string        `json:"path" yaml:"path"`
	Query  string        `json:"query" yaml:"query"`
	CSV    CSVConfig     `json:"csv" yaml:"csv"`
	JSON   JSONConfig    `json:"json" yaml:"json"`
	XML    XMLConfig     `json:"xml" yaml:"xml"`
	SQLite SQLiteConfig  `json:"sqlite" yaml:"sqlite"`
	MSSQL  MSSQLConfig   `json:"mssql" yaml:"mssql"`
	Auth   SQLAuthConfig `json:"auth" yaml:"auth"`
}

type OutputConfig struct {
	Type   string        `json:"type" yaml:"type"`
	Path   string        `json:"path" yaml:"path"`
	Table  string        `json:"table" yaml:"table"`
	CSV    CSVConfig     `json:"csv" yaml:"csv"`
	SQLite SQLiteConfig  `json:"sqlite" yaml:"sqlite"`
	MSSQL  MSSQLConfig   `json:"mssql" yaml:"mssql"`
	Auth   SQLAuthConfig `json:"auth" yaml:"auth"`
}

type ProcessingConfig struct {
	Mode                 string `json:"mode" yaml:"mode"`
	IncludeInputInOutput bool   `json:"include_input_in_output" yaml:"include_input_in_output"`
	ResponseField        string `json:"response_field" yaml:"response_field"`
	ContinueOnError      bool   `json:"continue_on_error" yaml:"continue_on_error"`
	Workers              int    `json:"workers" yaml:"workers"`
	MaxRetries           int    `json:"max_retries" yaml:"max_retries"`
	DryRun               bool   `json:"dry_run" yaml:"dry_run"`
}

type CSVConfig struct {
	Delimiter string `json:"delimiter" yaml:"delimiter"`
	HasHeader bool   `json:"has_header" yaml:"has_header"`
}

type JSONConfig struct {
	RootPath string `json:"root_path" yaml:"root_path"`
	JSONL    bool   `json:"jsonl" yaml:"jsonl"`
}

type XMLConfig struct {
	RecordPath string `json:"record_path" yaml:"record_path"`
}

type SQLiteConfig struct {
	DSN         string `json:"dsn" yaml:"dsn"`
	DSNEnv      string `json:"dsn_env" yaml:"dsn_env"`
	Table       string `json:"table" yaml:"table"`
	AutoMigrate bool   `json:"auto_migrate" yaml:"auto_migrate"`
}

type MSSQLConfig struct {
	DSN    string `json:"dsn" yaml:"dsn"`
	DSNEnv string `json:"dsn_env" yaml:"dsn_env"`
	Table  string `json:"table" yaml:"table"`
}

type SQLAuthConfig struct {
	UsernameEnv string `json:"username_env" yaml:"username_env"`
	PasswordEnv string `json:"password_env" yaml:"password_env"`
}

func Load(path string) (Config, error) {
	var cfg Config

	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(b, &cfg); err != nil {
			return cfg, fmt.Errorf("parse yaml: %w", err)
		}
	case ".json":
		if err := json.Unmarshal(b, &cfg); err != nil {
			return cfg, fmt.Errorf("parse json: %w", err)
		}
	default:
		return cfg, fmt.Errorf("unsupported config extension: %s", filepath.Ext(path))
	}

	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// providerDefaultBaseURL returns the canonical base URL for a known provider.
func providerDefaultBaseURL(provider string) string {
	switch strings.ToLower(provider) {
	case ProviderGemini:
		return "https://generativelanguage.googleapis.com/v1beta"
	case ProviderOllama:
		return "http://localhost:11434"
	case ProviderLMStudio:
		return "http://localhost:1234/v1"
	case ProviderAnthropic:
		return "https://api.anthropic.com/v1"
	default: // openai, generic, empty
		return "https://api.openai.com/v1"
	}
}

// ApplyDefaults fills in zero-value fields with sensible defaults.
func (c *Config) ApplyDefaults() {
	if c.API.Provider == "" {
		c.API.Provider = ProviderOpenAI
	}
	if c.API.BaseURL == "" {
		c.API.BaseURL = providerDefaultBaseURL(c.API.Provider)
	}
	if c.API.Timeout == 0 {
		c.API.Timeout = 60 * time.Second
	}
	if c.Processing.Mode == "" {
		c.Processing.Mode = "per_record"
	}
	if c.Processing.ResponseField == "" {
		c.Processing.ResponseField = "response"
	}
	if c.Processing.MaxRetries <= 0 {
		c.Processing.MaxRetries = 3
	}
	if c.Input.CSV.Delimiter == "" {
		c.Input.CSV.Delimiter = ","
	}
	if c.Output.CSV.Delimiter == "" {
		c.Output.CSV.Delimiter = ","
	}
}

func (c Config) Validate() error {
	var problems []error

	if c.API.Model == "" {
		problems = append(problems, errors.New("api.model is required"))
	}
	// Providers that work without an API key do not require api_key_env.
	nokeyProviders := map[string]bool{ProviderOllama: true, ProviderLMStudio: true}
	if !nokeyProviders[strings.ToLower(c.API.Provider)] && c.API.APIKeyEnv == "" {
		problems = append(problems, errors.New("api.api_key_env is required for this provider"))
	}
	if c.Input.Type == "" {
		problems = append(problems, errors.New("input.type is required"))
	}
	if c.Output.Type == "" {
		problems = append(problems, errors.New("output.type is required"))
	}
	if c.Prompt.InputTemplate == "" {
		problems = append(problems, errors.New("prompt.input_template is required"))
	}

	supportedInputs := map[string]bool{"csv": true, "json": true, "jsonl": true, "xml": true, "sqlite": true, "mssql": true}
	if !supportedInputs[strings.ToLower(c.Input.Type)] {
		problems = append(problems, fmt.Errorf("unsupported input.type: %s", c.Input.Type))
	}

	supportedOutputs := map[string]bool{"csv": true, "jsonl": true, "sqlite": true, "mssql": true}
	if !supportedOutputs[strings.ToLower(c.Output.Type)] {
		problems = append(problems, fmt.Errorf("unsupported output.type: %s", c.Output.Type))
	}

	if len(problems) > 0 {
		return errors.Join(problems...)
	}
	return nil
}

// APIKey resolves the API key. Returns empty string without error for providers that don't need a key.
func (c Config) APIKey() (string, error) {
	nokeyProviders := map[string]bool{ProviderOllama: true, ProviderLMStudio: true}
	if nokeyProviders[strings.ToLower(c.API.Provider)] {
		// Still honour the env var if the user sets one (e.g. secured Ollama deployment).
		if c.API.APIKeyEnv != "" {
			return strings.TrimSpace(os.Getenv(c.API.APIKeyEnv)), nil
		}
		return "", nil
	}
	v := strings.TrimSpace(os.Getenv(c.API.APIKeyEnv))
	if v == "" {
		return "", fmt.Errorf("environment variable %s is empty", c.API.APIKeyEnv)
	}
	return v, nil
}

func ResolveSecret(direct, envName string) string {
	if direct != "" {
		return direct
	}
	if envName == "" {
		return ""
	}
	return os.Getenv(envName)
}
