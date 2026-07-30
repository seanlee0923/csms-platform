package redisstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/seanlee0923/csms-platform/internal/sessionregistry"
)

var claimScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 1 then
	return {0, redis.call("HGET", KEYS[1], "owner"), redis.call("HGET", KEYS[1], "generation"), redis.call("PTTL", KEYS[1])}
end
local generation = redis.call("INCR", KEYS[2])
redis.call("HSET", KEYS[1], "owner", ARGV[1], "generation", generation)
redis.call("PEXPIRE", KEYS[1], ARGV[2])
return {1, ARGV[1], generation, tonumber(ARGV[2])}
`)

var renewScript = redis.NewScript(`
if redis.call("HGET", KEYS[1], "owner") ~= ARGV[1] or redis.call("HGET", KEYS[1], "generation") ~= ARGV[2] then
	return 0
end
redis.call("PEXPIRE", KEYS[1], ARGV[3])
return 1
`)

var releaseScript = redis.NewScript(`
if redis.call("HGET", KEYS[1], "owner") ~= ARGV[1] or redis.call("HGET", KEYS[1], "generation") ~= ARGV[2] then
	return 0
end
return redis.call("DEL", KEYS[1])
`)

type Registry struct {
	client redis.UniversalClient
	prefix string
	now    func() time.Time
}

func New(client redis.UniversalClient, prefix string) (*Registry, error) {
	if client == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	if prefix == "" {
		prefix = "csms"
	}
	return &Registry{client: client, prefix: prefix, now: time.Now}, nil
}

func (registry *Registry) Claim(ctx context.Context, stationIdentity, ownerID string, ttl time.Duration) (sessionregistry.Lease, error) {
	if stationIdentity == "" || ownerID == "" || ttl <= 0 {
		return sessionregistry.Lease{}, fmt.Errorf("station identity, owner ID and positive TTL are required")
	}
	result, err := claimScript.Run(ctx, registry.client,
		[]string{registry.leaseKey(stationIdentity), registry.generationKey()},
		ownerID, ttl.Milliseconds(),
	).Slice()
	if err != nil {
		return sessionregistry.Lease{}, fmt.Errorf("claim redis session lease: %w", err)
	}
	claimed, owner, generation, remaining, err := parseClaimResult(result)
	if err != nil {
		return sessionregistry.Lease{}, err
	}
	lease := sessionregistry.Lease{
		StationIdentity: stationIdentity, OwnerID: owner, Generation: generation,
		ExpiresAt: registry.now().UTC().Add(remaining),
	}
	if !claimed {
		return lease, fmt.Errorf("%w: owner %s generation %d", sessionregistry.ErrOwnershipConflict, owner, generation)
	}
	return lease, nil
}

func (registry *Registry) Renew(ctx context.Context, lease sessionregistry.Lease, ttl time.Duration) (sessionregistry.Lease, error) {
	if ttl <= 0 {
		return sessionregistry.Lease{}, fmt.Errorf("lease TTL must be positive")
	}
	result, err := renewScript.Run(ctx, registry.client, []string{registry.leaseKey(lease.StationIdentity)},
		lease.OwnerID, lease.Generation, ttl.Milliseconds(),
	).Int()
	if err != nil {
		return sessionregistry.Lease{}, fmt.Errorf("renew redis session lease: %w", err)
	}
	if result != 1 {
		return sessionregistry.Lease{}, sessionregistry.ErrLeaseLost
	}
	lease.ExpiresAt = registry.now().UTC().Add(ttl)
	return lease, nil
}

func (registry *Registry) Release(ctx context.Context, lease sessionregistry.Lease) error {
	result, err := releaseScript.Run(ctx, registry.client, []string{registry.leaseKey(lease.StationIdentity)},
		lease.OwnerID, lease.Generation,
	).Int()
	if err != nil {
		return fmt.Errorf("release redis session lease: %w", err)
	}
	if result != 1 {
		return sessionregistry.ErrLeaseLost
	}
	return nil
}

func (registry *Registry) Lookup(ctx context.Context, stationIdentity string) (sessionregistry.Lease, error) {
	values, err := registry.client.HMGet(ctx, registry.leaseKey(stationIdentity), "owner", "generation").Result()
	if err != nil {
		return sessionregistry.Lease{}, fmt.Errorf("lookup redis session lease: %w", err)
	}
	remaining, err := registry.client.PTTL(ctx, registry.leaseKey(stationIdentity)).Result()
	if err != nil {
		return sessionregistry.Lease{}, fmt.Errorf("lookup redis session TTL: %w", err)
	}
	if len(values) != 2 || values[0] == nil || values[1] == nil || remaining <= 0 {
		return sessionregistry.Lease{}, sessionregistry.ErrNotFound
	}
	generation, err := strconv.ParseUint(fmt.Sprint(values[1]), 10, 64)
	if err != nil {
		return sessionregistry.Lease{}, fmt.Errorf("parse redis session generation: %w", err)
	}
	return sessionregistry.Lease{
		StationIdentity: stationIdentity, OwnerID: fmt.Sprint(values[0]), Generation: generation,
		ExpiresAt: registry.now().UTC().Add(remaining),
	}, nil
}

func (registry *Registry) leaseKey(identity string) string {
	return registry.prefix + ":session:" + identity
}

func (registry *Registry) generationKey() string {
	return registry.prefix + ":session:generation"
}

func parseClaimResult(result []any) (bool, string, uint64, time.Duration, error) {
	if len(result) != 4 {
		return false, "", 0, 0, errors.New("invalid Redis claim result")
	}
	status, err := strconv.ParseInt(fmt.Sprint(result[0]), 10, 64)
	if err != nil {
		return false, "", 0, 0, fmt.Errorf("parse Redis claim status: %w", err)
	}
	generation, err := strconv.ParseUint(fmt.Sprint(result[2]), 10, 64)
	if err != nil {
		return false, "", 0, 0, fmt.Errorf("parse Redis claim generation: %w", err)
	}
	milliseconds, err := strconv.ParseInt(fmt.Sprint(result[3]), 10, 64)
	if err != nil {
		return false, "", 0, 0, fmt.Errorf("parse Redis claim TTL: %w", err)
	}
	return status == 1, fmt.Sprint(result[1]), generation, time.Duration(milliseconds) * time.Millisecond, nil
}
