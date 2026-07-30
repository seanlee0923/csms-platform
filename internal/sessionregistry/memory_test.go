package sessionregistry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryClaimRenewRelease(t *testing.T) {
	now := time.Now().UTC()
	registry := NewMemory()
	registry.now = func() time.Time { return now }
	lease, err := registry.Claim(context.Background(), "station-1", "runtime-a", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Generation == 0 || !lease.ExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("unexpected lease: %+v", lease)
	}
	if _, err := registry.Claim(context.Background(), "station-1", "runtime-b", 30*time.Second); !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("claim conflict = %v", err)
	}
	now = now.Add(10 * time.Second)
	renewed, err := registry.Renew(context.Background(), lease, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.ExpiresAt.Equal(now.Add(30 * time.Second)) {
		t.Fatalf("unexpected renewed lease: %+v", renewed)
	}
	if err := registry.Release(context.Background(), renewed); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Lookup(context.Background(), "station-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("lookup after release = %v", err)
	}
}

func TestMemoryExpirationCreatesHigherFencingGeneration(t *testing.T) {
	now := time.Now().UTC()
	registry := NewMemory()
	registry.now = func() time.Time { return now }
	oldLease, err := registry.Claim(context.Background(), "station-1", "runtime-a", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	newLease, err := registry.Claim(context.Background(), "station-1", "runtime-b", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if newLease.Generation <= oldLease.Generation {
		t.Fatalf("generation did not increase: old=%d new=%d", oldLease.Generation, newLease.Generation)
	}
	if err := registry.Release(context.Background(), oldLease); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale release = %v", err)
	}
	if current, err := registry.Lookup(context.Background(), "station-1"); err != nil || current != newLease {
		t.Fatalf("new owner was changed: %+v, %v", current, err)
	}
}
