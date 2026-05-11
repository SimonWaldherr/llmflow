package llm

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const minRateLimitDelay = 5 * time.Second

// RateLimitError marks provider responses that should be retried with a slower backoff.
type RateLimitError struct {
	Provider   string
	StatusCode int
	Message    string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	prefix := "api rate limit"
	if e.Provider != "" {
		prefix = e.Provider + " " + prefix
	}
	if e.StatusCode > 0 {
		prefix = fmt.Sprintf("%s status %d", prefix, e.StatusCode)
	}
	if e.Message != "" {
		prefix += ": " + e.Message
	}
	if e.RetryAfter > 0 {
		prefix += fmt.Sprintf(" (retry after %s)", e.RetryAfter.Round(time.Second))
	}
	return prefix
}

func IsRateLimit(err error) bool {
	var rateLimitErr *RateLimitError
	return errors.As(err, &rateLimitErr)
}

func RetryDelay(err error, attempt int) time.Duration {
	delay := Backoff(attempt)

	var rateLimitErr *RateLimitError
	if !errors.As(err, &rateLimitErr) {
		return delay
	}
	if delay < minRateLimitDelay {
		delay = minRateLimitDelay
	}
	if rateLimitErr.RetryAfter > delay {
		return rateLimitErr.RetryAfter
	}
	return delay
}

func apiStatusError(provider string, resp *http.Response, body []byte) error {
	message := strings.TrimSpace(string(body))
	if isRateLimited(resp.StatusCode, message) {
		return &RateLimitError{
			Provider:   provider,
			StatusCode: resp.StatusCode,
			Message:    message,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	if provider != "" {
		return fmt.Errorf("%s api status %d: %s", provider, resp.StatusCode, message)
	}
	return fmt.Errorf("api status %d: %s", resp.StatusCode, message)
}

func apiResponseError(provider string, code int, errorType, message string) error {
	formatted := strings.TrimSpace(message)
	if strings.TrimSpace(errorType) != "" {
		formatted = fmt.Sprintf("(%s): %s", strings.TrimSpace(errorType), formatted)
	}
	if isRateLimited(code, errorType+" "+message) {
		return &RateLimitError{
			Provider:   provider,
			StatusCode: code,
			Message:    formatted,
		}
	}
	if provider != "" {
		if strings.TrimSpace(errorType) != "" {
			return fmt.Errorf("%s api error (%s): %s", provider, strings.TrimSpace(errorType), strings.TrimSpace(message))
		}
		return fmt.Errorf("%s api error: %s", provider, strings.TrimSpace(message))
	}
	if strings.TrimSpace(errorType) != "" {
		return fmt.Errorf("api error (%s): %s", strings.TrimSpace(errorType), strings.TrimSpace(message))
	}
	return fmt.Errorf("api error: %s", strings.TrimSpace(message))
}

func isRateLimited(code int, text string) bool {
	if code == http.StatusTooManyRequests {
		return true
	}
	lowerText := strings.ToLower(text)
	return strings.Contains(lowerText, "too_many_requests") ||
		strings.Contains(lowerText, "rate limit") ||
		strings.Contains(lowerText, "rate_limit")
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	delay := time.Until(when)
	if delay <= 0 {
		return 0
	}
	return delay
}