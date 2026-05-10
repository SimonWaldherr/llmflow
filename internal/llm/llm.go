// Package llm provides a unified Generator interface for various LLM providers
// and a factory that selects the right implementation based on the config.
package llm

import (
	"context"
	"time"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

// Generator is the common interface for calling any LLM provider.
type Generator interface {
	Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// New returns the appropriate Generator for the provider declared in cfg.
// Supported providers: openai, azure, gemini, ollama, lmstudio, anthropic, generic.
func New(cfg config.APIConfig, apiKey string) Generator {
	switch cfg.Provider {
	case config.ProviderGemini:
		return newGeminiClient(cfg, apiKey)
	case config.ProviderOllama:
		return newOllamaClient(cfg)
	case config.ProviderAzure, config.ProviderLMStudio, config.ProviderGeneric:
		return newOpenAICompatClient(cfg, apiKey)
	case config.ProviderAnthropic:
		return newAnthropicClient(cfg, apiKey)
	default: // openai + anything unknown defaults to OpenAI chat completions wire format
		return newOpenAICompatClient(cfg, apiKey)
	}
}

// Backoff returns an exponential back-off duration for the given attempt number (1-based).
func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<uint(attempt-1)) * time.Second
}
