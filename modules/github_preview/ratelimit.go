package github_preview

import (
	"context"

	"golang.org/x/time/rate"
)

// RateLimiter wraps a token bucket rate limiter for GitHub API calls.
// Critical paths (artifact resolution) block waiting for a token.
// Non-critical paths (debug endpoint) try immediately and fail fast.
type RateLimiter struct {
	limiter *rate.Limiter
}

func newRateLimiter(rps float64, burst int) *RateLimiter {
	return &RateLimiter{
		limiter: rate.NewLimiter(rate.Limit(rps), burst),
	}
}

// wait blocks until a token is available or the context is cancelled.
// use for critical paths like artifact resolution.
func (r *RateLimiter) wait(ctx context.Context) error {
	return r.limiter.Wait(ctx)
}

// tryAcquire attempts to take a token without blocking.
// returns true if a token was acquired, false if rate limited.
// use for non-critical paths like the debug endpoint.
func (r *RateLimiter) tryAcquire() bool {
	return r.limiter.Allow()
}
