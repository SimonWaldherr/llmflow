package llm

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestAPIStatusErrorDetectsRateLimit(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     make(http.Header),
	}
	resp.Header.Set("Retry-After", "7")

	err := apiStatusError("openai", resp, []byte(`{"error":{"type":"too_many_requests","message":"slow down"}}`))
	if !IsRateLimit(err) {
		t.Fatalf("expected rate-limit error, got %v", err)
	}

	var rateLimitErr *RateLimitError
	if !errors.As(err, &rateLimitErr) {
		t.Fatalf("expected RateLimitError, got %T", err)
	}
	if rateLimitErr.RetryAfter != 7*time.Second {
		t.Fatalf("unexpected retry-after: got %v want %v", rateLimitErr.RetryAfter, 7*time.Second)
	}
}

func TestAPIResponseErrorDetectsTooManyRequests(t *testing.T) {
	err := apiResponseError("openai", 0, "too_many_requests", "slow down")
	if !IsRateLimit(err) {
		t.Fatalf("expected rate-limit error, got %v", err)
	}
}

func TestRetryDelayUsesRateLimitFloor(t *testing.T) {
	err := &RateLimitError{Provider: "openai", Message: "slow down"}
	if got := RetryDelay(err, 1); got != 5*time.Second {
		t.Fatalf("RetryDelay() = %v, want %v", got, 5*time.Second)
	}
}

func TestRetryDelayUsesRetryAfterWhenLonger(t *testing.T) {
	err := &RateLimitError{Provider: "openai", Message: "slow down", RetryAfter: 9 * time.Second}
	if got := RetryDelay(err, 1); got != 9*time.Second {
		t.Fatalf("RetryDelay() = %v, want %v", got, 9*time.Second)
	}
}