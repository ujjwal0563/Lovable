package middleware

import (
	"net/http"
	"sync"
	"time"
)

// tokenBucket holds the state for one user's rate limit.
type tokenBucket struct {
	tokens    float64
	lastRefil time.Time
	mu        sync.Mutex
}

// RateLimiter holds per-user buckets.
type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*tokenBucket
	rate     float64 // tokens per second
	capacity float64 // max burst
}

// NewRateLimiter creates a limiter with `reqsPerMin` requests per minute capacity.
func NewRateLimiter(reqsPerMin float64) *RateLimiter {
	rl := &RateLimiter{
		buckets:  make(map[string]*tokenBucket),
		rate:     reqsPerMin / 60.0,
		capacity: reqsPerMin,
	}
	// Clean up old buckets every 5 minutes
	go func() {
		for range time.Tick(5 * time.Minute) {
			rl.cleanup()
		}
	}()
	return rl
}

func (rl *RateLimiter) getBucket(userID string) *tokenBucket {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b, ok := rl.buckets[userID]
	if !ok {
		b = &tokenBucket{tokens: rl.capacity, lastRefil: time.Now()}
		rl.buckets[userID] = b
	}
	return b
}

func (rl *RateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := time.Now().Add(-10 * time.Minute)
	for k, b := range rl.buckets {
		b.mu.Lock()
		stale := b.lastRefil.Before(cutoff)
		b.mu.Unlock()
		if stale {
			delete(rl.buckets, k)
		}
	}
}

// Allow returns true if the user is within the rate limit.
func (rl *RateLimiter) Allow(userID string) bool {
	b := rl.getBucket(userID)
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.lastRefil).Seconds()
	b.tokens += elapsed * rl.rate
	if b.tokens > rl.capacity {
		b.tokens = rl.capacity
	}
	b.lastRefil = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Middleware returns an HTTP middleware that rate-limits by authenticated user ID.
// Falls back to IP if no user in context.
func (rl *RateLimiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := GetUserID(r)
			if key == "" {
				key = r.RemoteAddr
			}
			if !rl.Allow(key) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"error":"rate limit exceeded, slow down"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
