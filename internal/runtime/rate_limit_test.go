package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisCommandRateLimiterSharesCounter(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { client.Close() })
	limiter := &redisCommandRateLimiter{
		client: client, prefix: "test", limit: 2, window: time.Minute,
	}
	for attempt := 1; attempt <= 3; attempt++ {
		allowed, err := limiter.Allow(context.Background(), "credential")
		if err != nil {
			t.Fatal(err)
		}
		if allowed != (attempt <= 2) {
			t.Fatalf("attempt %d allowed = %v", attempt, allowed)
		}
	}
}
