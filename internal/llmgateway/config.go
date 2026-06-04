package llmgateway

import (
"errors"
"fmt"
"os"
"slices"
"strings"
"time"

"gopkg.in/yaml.v3"
)

type Config struct {
Servers  ServerConfig  `yaml:"servers"`
Health   HealthConfig  `yaml:"health"`
Defaults DefaultConfig `yaml:"defaults"`
Backends []BackendSpec `yaml:"backends"`
Routes   []RouteSpec   `yaml:"routes"`
}

type ServerConfig struct {
OpenAIAddr string `yaml:"openai_addr"`
OllamaAddr string `yaml:"ollama_addr"`
}

type HealthConfig struct {
Interval time.Duration `yaml:"interval"`
Timeout  time.Duration `yaml:"timeout"`
}

type DefaultConfig struct {
Strategy string   `yaml:"strategy"`
Backends []string `yaml:"backends"`
Fallback []string `yaml:"fallback"`
}

type BackendSpec struct {
Name          string            `yaml:"name"`
URL           string            `yaml:"url"`
Weight        int               `yaml:"weight"`
Priority      int               `yaml:"priority"`
ContextLength int               `yaml:"context_length"`
HealthPath    string            `yaml:"health_path"`
Timeout       time.Duration     `yaml:"timeout"`
Type          string            `yaml:"type"`
Models        []string          `yaml:"models"`
ModelMap      map[string]string `yaml:"model_map"`
Headers       map[string]string `yaml:"headers"`
Capabilities  []string          `yaml:"capabilities"`
Tags          []string          `yaml:"tags"`
CostPer1M     float64           `yaml:"cost_per_1m"`
Metadata      map[string]string `yaml:"metadata"`
Disabled      bool              `yaml:"disabled"`
}

type RouteSpec struct {
Name      string               `yaml:"name"`
Priority  int                  `yaml:"priority"`
Strategy  string               `yaml:"strategy"`
Backends  []string             `yaml:"backends"`
Fallback  []string             `yaml:"fallback"`
Keywords  []string             `yaml:"keywords"`
MinTokens int                  `yaml:"min_tokens"`
MaxTokens int                  `yaml:"max_tokens"`
Paths     []string             `yaml:"paths"`
Conditions []ConditionSpec     `yaml:"conditions"`
Classifier *LLMClassifierSpec  `yaml:"classifier"`
}

type ConditionSpec struct {
Field    string `yaml:"field"`
Operator string `yaml:"operator"`
Value    string `yaml:"value"`
}

type LLMClassifierSpec struct {
Backend string              `yaml:"backend"`
Prompt  string              `yaml:"prompt"`
Timeout time.Duration       `yaml:"timeout"`
Routes  map[string][]string `yaml:"routes"`
}

func LoadConfig(path string) (Config, error) {
data, err := os.ReadFile(path)
if err != nil {
return Config{}, fmt.Errorf("read config: %w", err)
}
var cfg Config
if err := yaml.Unmarshal(data, &cfg); err != nil {
return Config{}, fmt.Errorf("parse yaml: %w", err)
}
cfg.applyDefaults()
if err := cfg.Validate(); err != nil {
return Config{}, err
}
return cfg, nil
}

func (c *Config) applyDefaults() {
if c.Servers.OpenAIAddr == "" {
c.Servers.OpenAIAddr = ":1234"
}
if c.Servers.OllamaAddr == "" {
c.Servers.OllamaAddr = ":11434"
}
if c.Health.Interval <= 0 {
c.Health.Interval = 15 * time.Second
}
if c.Health.Timeout <= 0 {
c.Health.Timeout = 3 * time.Second
}
if c.Defaults.Strategy == "" {
c.Defaults.Strategy = StrategyWeightedRoundRobin
}
for i := range c.Backends {
if c.Backends[i].Weight <= 0 {
c.Backends[i].Weight = 1
}
if c.Backends[i].Timeout <= 0 {
c.Backends[i].Timeout = 90 * time.Second
}
if c.Backends[i].HealthPath == "" {
if strings.EqualFold(c.Backends[i].Type, "ollama") {
c.Backends[i].HealthPath = "/api/tags"
} else {
c.Backends[i].HealthPath = "/v1/models"
}
}
}
for i := range c.Routes {
if c.Routes[i].Strategy == "" {
c.Routes[i].Strategy = c.Defaults.Strategy
}
}
}

func (c Config) Validate() error {
if len(c.Backends) == 0 {
return errors.New("at least one backend is required")
}
knownBackends := make(map[string]struct{}, len(c.Backends))
for _, b := range c.Backends {
if b.Name == "" {
return errors.New("backend.name is required")
}
if b.URL == "" {
return fmt.Errorf("backend %q: url is required", b.Name)
}
if _, ok := knownBackends[b.Name]; ok {
return fmt.Errorf("backend %q duplicated", b.Name)
}
knownBackends[b.Name] = struct{}{}
}
if err := validateBackendRefs("defaults.backends", c.Defaults.Backends, knownBackends); err != nil {
return err
}
if err := validateBackendRefs("defaults.fallback", c.Defaults.Fallback, knownBackends); err != nil {
return err
}
for i, r := range c.Routes {
if r.Name == "" {
return fmt.Errorf("routes[%d].name is required", i)
}
if err := validateStrategy(r.Strategy); err != nil {
return fmt.Errorf("routes[%d]: %w", i, err)
}
if err := validateBackendRefs(fmt.Sprintf("routes[%d].backends", i), r.Backends, knownBackends); err != nil {
return err
}
if err := validateBackendRefs(fmt.Sprintf("routes[%d].fallback", i), r.Fallback, knownBackends); err != nil {
return err
}
if r.MinTokens > 0 && r.MaxTokens > 0 && r.MinTokens > r.MaxTokens {
return fmt.Errorf("routes[%d]: min_tokens must be <= max_tokens", i)
}
if r.Classifier != nil {
if r.Classifier.Backend != "" {
if _, ok := knownBackends[r.Classifier.Backend]; !ok {
return fmt.Errorf("routes[%d].classifier.backend references unknown backend %q", i, r.Classifier.Backend)
}
}
for label, refs := range r.Classifier.Routes {
if label == "" {
return fmt.Errorf("routes[%d].classifier.routes contains empty label", i)
}
if err := validateBackendRefs(fmt.Sprintf("routes[%d].classifier.routes[%q]", i, label), refs, knownBackends); err != nil {
return err
}
}
}
}
if err := validateStrategy(c.Defaults.Strategy); err != nil {
return err
}
return nil
}

func validateBackendRefs(name string, refs []string, known map[string]struct{}) error {
for _, ref := range refs {
if _, ok := known[ref]; !ok {
return fmt.Errorf("%s references unknown backend %q", name, ref)
}
}
return nil
}

func validateStrategy(name string) error {
s := strings.ToLower(strings.TrimSpace(name))
if s == "" {
return nil
}
if !slices.Contains([]string{StrategyWeightedRoundRobin, StrategyLeastConnections}, s) {
return fmt.Errorf("unsupported strategy %q", name)
}
return nil
}
