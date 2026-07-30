package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var commandRateScript = redis.NewScript(`
local count = redis.call("INCR", KEYS[1])
if count == 1 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return count
`)

type commandRateLimiter interface {
	Allow(context.Context, string) (bool, error)
}

type redisCommandRateLimiter struct {
	client redis.UniversalClient
	prefix string
	limit  int64
	window time.Duration
}

func (limiter *redisCommandRateLimiter) Allow(ctx context.Context, subject string) (bool, error) {
	windowID := time.Now().UTC().UnixNano() / limiter.window.Nanoseconds()
	key := fmt.Sprintf("%s:command-rate:%s:%d", limiter.prefix, subject, windowID)
	count, err := commandRateScript.Run(ctx, limiter.client, []string{key}, limiter.window.Milliseconds()).Int64()
	if err != nil {
		return false, fmt.Errorf("apply Redis command rate limit: %w", err)
	}
	return count <= limiter.limit, nil
}
