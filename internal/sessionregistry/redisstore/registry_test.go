package redisstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/seanlee0923/csms-platform/internal/sessionregistry"
)

func TestRedisRegistryOwnershipLifecycleAndFencing(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { client.Close() })
	registry, err := New(client, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := registry.Claim(ctx, "station-1", "runtime-a", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Claim(ctx, "station-1", "runtime-b", 30*time.Second); !errors.Is(err, sessionregistry.ErrOwnershipConflict) {
		t.Fatalf("conflicting claim = %v", err)
	}
	renewed, err := registry.Renew(ctx, first, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := registry.Lookup(ctx, "station-1"); err != nil || current.OwnerID != "runtime-a" || current.Generation != renewed.Generation {
		t.Fatalf("lookup = %+v, %v", current, err)
	}
	server.FastForward(31 * time.Second)
	second, err := registry.Claim(ctx, "station-1", "runtime-b", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation <= first.Generation {
		t.Fatalf("generation did not increase: first=%d second=%d", first.Generation, second.Generation)
	}
	if err := registry.Release(ctx, first); !errors.Is(err, sessionregistry.ErrLeaseLost) {
		t.Fatalf("stale release = %v", err)
	}
	if err := registry.Release(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Lookup(ctx, "station-1"); !errors.Is(err, sessionregistry.ErrNotFound) {
		t.Fatalf("lookup after release = %v", err)
	}
}
