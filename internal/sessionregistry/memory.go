package sessionregistry

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type Memory struct {
	mu         sync.Mutex
	generation uint64
	leases     map[string]Lease
	now        func() time.Time
}

func NewMemory() *Memory {
	return &Memory{leases: make(map[string]Lease), now: time.Now}
}

func (registry *Memory) Claim(ctx context.Context, stationIdentity, ownerID string, ttl time.Duration) (Lease, error) {
	if err := validate(ctx, stationIdentity, ownerID, ttl); err != nil {
		return Lease{}, err
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	now := registry.now().UTC()
	if current, ok := registry.leases[stationIdentity]; ok && current.ExpiresAt.After(now) {
		return current, fmt.Errorf("%w: owner %s generation %d", ErrOwnershipConflict, current.OwnerID, current.Generation)
	}
	registry.generation++
	lease := Lease{
		StationIdentity: stationIdentity, OwnerID: ownerID, Generation: registry.generation,
		ExpiresAt: now.Add(ttl),
	}
	registry.leases[stationIdentity] = lease
	return lease, nil
}

func (registry *Memory) Renew(ctx context.Context, lease Lease, ttl time.Duration) (Lease, error) {
	if err := validate(ctx, lease.StationIdentity, lease.OwnerID, ttl); err != nil {
		return Lease{}, err
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	now := registry.now().UTC()
	current, ok := registry.leases[lease.StationIdentity]
	if !ok || !current.ExpiresAt.After(now) || current.OwnerID != lease.OwnerID || current.Generation != lease.Generation {
		delete(registry.leases, lease.StationIdentity)
		return Lease{}, ErrLeaseLost
	}
	current.ExpiresAt = now.Add(ttl)
	registry.leases[lease.StationIdentity] = current
	return current, nil
}

func (registry *Memory) Release(ctx context.Context, lease Lease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	current, ok := registry.leases[lease.StationIdentity]
	if !ok {
		return nil
	}
	if current.OwnerID != lease.OwnerID || current.Generation != lease.Generation {
		return ErrLeaseLost
	}
	delete(registry.leases, lease.StationIdentity)
	return nil
}

func (registry *Memory) Lookup(ctx context.Context, stationIdentity string) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}
	if stationIdentity == "" {
		return Lease{}, fmt.Errorf("station identity is required")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	lease, ok := registry.leases[stationIdentity]
	if !ok || !lease.ExpiresAt.After(registry.now().UTC()) {
		delete(registry.leases, stationIdentity)
		return Lease{}, ErrNotFound
	}
	return lease, nil
}

func validate(ctx context.Context, stationIdentity, ownerID string, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if stationIdentity == "" || ownerID == "" {
		return fmt.Errorf("station identity and owner ID are required")
	}
	if ttl <= 0 {
		return fmt.Errorf("lease TTL must be positive")
	}
	return nil
}
