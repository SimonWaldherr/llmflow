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
	Tools      ToolsConfig      `json:"tools" yaml:"tools"`
}

// ToolsConfig controls optional agent tools the LLM may call during processing.
// When Enabled is true and at least one tool is active the app uses an agentic
// loop: the LLM may invoke tools (web fetch/search, link extraction,
// sandboxed code execution, SQL queries) to gather information before
// returning a final answer.
type ToolsConfig struct {
	// Enabled activates the agentic tool-calling loop.
	Enabled bool `json:"enabled" yaml:"enabled"`
	// WebFetch allows the LLM to fetch the content of arbitrary URLs.
	WebFetch bool `json:"web_fetch" yaml:"web_fetch"`
	// WebSearch allows the LLM to search the web via DuckDuckGo.
	WebSearch bool `json:"web_search" yaml:"web_search"`
	// WebExtractLinks allows the LLM to extract absolute links from a web page.
	WebExtractLinks bool `json:"web_extract_links" yaml:"web_extract_links"`
	// CodeExecute allows the LLM to run sandboxed Go code via nanoGo.
	CodeExecute bool `json:"code_execute" yaml:"code_execute"`
	// Code configures the sandbox behavior for code_execute.
	Code ToolsCodeConfig `json:"code" yaml:"code"`
	// SQLQuery allows the LLM to run read-only SQL queries against a database.
	SQLQuery bool `json:"sql_query" yaml:"sql_query"`
	// SQL configures the database connection for the sql_query tool.
	SQL ToolsSQLConfig `json:"sql" yaml:"sql"`
	// MaxRounds caps the number of tool-call / result iterations per record
	// (default 5 when zero).
	MaxRounds int `json:"max_rounds" yaml:"max_rounds"`
}

// ToolsSQLConfig holds the connection details for the sql_query tool.
type ToolsSQLConfig struct {
	// Driver selects the database driver: "sqlite" or "sqlserver".
	Driver string `json:"driver" yaml:"driver"`
	// DSN is the data-source name (connection string).
	DSN string `json:"dsn" yaml:"dsn"`
	// DSNEnv is the name of an environment variable that holds the DSN.
	// If both DSN and DSNEnv are set, DSN takes precedence.
	DSNEnv string `json:"dsn_env" yaml:"dsn_env"`
}

// ToolsCodeConfig controls sandboxed code execution via nanoGo.
type ToolsCodeConfig struct {
	// Timeout limits one interpreted program execution.
	Timeout time.Duration `json:"timeout" yaml:"timeout"`
	// MaxSourceBytes limits the source size accepted by the tool.
	MaxSourceBytes int `json:"max_source_bytes" yaml:"max_source_bytes"`
	// ReadOnlyFS enables HostReadFile(path) native for whitelisted relative paths.
	ReadOnlyFS bool `json:"read_only_fs" yaml:"read_only_fs"`
	// ReadWhitelist lists allowed files/folders relative to repo root.
	ReadWhitelist []string `json:"read_whitelist" yaml:"read_whitelist"`
	// HTTPGet enables HTTPGetText(url) native.
	HTTPGet bool `json:"http_get" yaml:"http_get"`
	// HTTPTimeout limits each HTTPGetText call.
	HTTPTimeout time.Duration `json:"http_timeout" yaml:"http_timeout"`
	// HTTPMinInterval enforces delay between HTTPGetText calls.
	HTTPMinInterval time.Duration `json:"http_min_interval" yaml:"http_min_interval"`
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
	// PromptCaching enables provider-side prompt caching for the static parts of each
	// request (system prompt, pre-prompt, post-prompt).
	// Supported: anthropic (cache_control blocks), openai (automatic prefix caching + beta header).
	PromptCaching bool `json:"prompt_caching" yaml:"prompt_caching"`
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
	c.API.ApplyProviderDefaults()
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
	if c.Tools.MaxRounds <= 0 {
		c.Tools.MaxRounds = 5
	}
	if c.Tools.Code.Timeout <= 0 {
		c.Tools.Code.Timeout = 10 * time.Second
	}
	if c.Tools.Code.MaxSourceBytes <= 0 {
		c.Tools.Code.MaxSourceBytes = 64 * 1024
	}
	if c.Tools.Code.HTTPTimeout <= 0 {
		c.Tools.Code.HTTPTimeout = 5 * time.Second
	}
	if c.Tools.Code.HTTPMinInterval <= 0 {
		c.Tools.Code.HTTPMinInterval = 200 * time.Millisecond
	}
	if len(c.Tools.Code.ReadWhitelist) == 0 {
		c.Tools.Code.ReadWhitelist = []string{"examples", "README.md", "LICENSE"}
	}
}

// ApplyProviderDefaults fills in zero-value API-level fields with sensible defaults.
// This can be called independently of the full Config (e.g. for one-off LLM calls).
func (c *APIConfig) ApplyProviderDefaults() {
	if c.Provider == "" {
		c.Provider = ProviderOpenAI
	}
	if c.BaseURL == "" {
		c.BaseURL = providerDefaultBaseURL(c.Provider)
	}
	if c.Timeout == 0 {
		c.Timeout = 300 * time.Second // 5 min; LLMs can be slow, especially local models
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
