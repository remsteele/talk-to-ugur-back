package middleware

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"talk-to-ugur-back/config"
)

type RateLimiter struct {
	mu            sync.Mutex
	clients       map[string]*clientLimiter
	rate          rate.Limit
	burst         int
	window        time.Duration
	blockDuration time.Duration
	maxStrikes    int
	enabled       bool
	lastCleanup   time.Time
}

type clientLimiter struct {
	limiter      *rate.Limiter
	lastSeen     time.Time
	blockedUntil time.Time
	strikes      int
}

func NewRateLimiter(cfg *config.Config) *RateLimiter {
	window := time.Duration(cfg.RateLimitWindowSeconds) * time.Second
	if window <= 0 {
		window = time.Minute
	}
	requests := cfg.RateLimitRequests
	if requests <= 0 {
		requests = 60
	}
	burst := cfg.RateLimitBurst
	if burst <= 0 {
		burst = 10
	}
	blockDuration := time.Duration(cfg.RateLimitBlockSeconds) * time.Second
	if blockDuration <= 0 {
		blockDuration = 10 * time.Minute
	}
	maxStrikes := cfg.RateLimitMaxStrikes
	if maxStrikes <= 0 {
		maxStrikes = 5
	}

	ratePerSecond := rate.Every(window / time.Duration(requests))
	return &RateLimiter{
		clients:       make(map[string]*clientLimiter),
		rate:          ratePerSecond,
		burst:         burst,
		window:        window,
		blockDuration: blockDuration,
		maxStrikes:    maxStrikes,
		enabled:       cfg.RateLimitEnabled,
		lastCleanup:   time.Now(),
	}
}

func RateLimitMiddleware(limiter *RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if limiter == nil || !limiter.enabled {
			c.Next()
			return
		}
		if c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}

		key := "ip:" + strings.TrimSpace(c.ClientIP())

		if blocked, retryAfter := limiter.allow(key); !blocked {
			c.Next()
			return
		} else {
			c.Header("Retry-After", retryAfter)
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":       "rate limit exceeded",
				"retry_after": retryAfter,
			})
			c.Abort()
			return
		}
	}
}

func (r *RateLimiter) allow(key string) (blocked bool, retryAfter string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	r.cleanup(now)

	cl, exists := r.clients[key]
	if !exists {
		cl = &clientLimiter{
			limiter:  rate.NewLimiter(r.rate, r.burst),
			lastSeen: now,
		}
		r.clients[key] = cl
	}

	cl.lastSeen = now
	if now.Before(cl.blockedUntil) {
		return true, r.retryAfter(cl.blockedUntil, now)
	}

	if cl.limiter.Allow() {
		if cl.strikes > 0 {
			cl.strikes = 0
		}
		return false, ""
	}

	cl.strikes++
	if cl.strikes >= r.maxStrikes {
		cl.blockedUntil = now.Add(r.blockDuration)
		cl.strikes = 0
		return true, r.retryAfter(cl.blockedUntil, now)
	}

	return true, r.retryAfter(now.Add(r.window), now)
}

func (r *RateLimiter) retryAfter(until, now time.Time) string {
	remaining := until.Sub(now)
	if remaining < time.Second {
		remaining = time.Second
	}
	return time.Duration(remaining.Seconds() + 0.5).Truncate(time.Second).String()
}

func (r *RateLimiter) cleanup(now time.Time) {
	if now.Sub(r.lastCleanup) < time.Minute {
		return
	}
	for key, cl := range r.clients {
		if now.Sub(cl.lastSeen) > 2*r.window && now.After(cl.blockedUntil) {
			delete(r.clients, key)
		}
	}
	r.lastCleanup = now
}
