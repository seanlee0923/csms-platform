package runtime

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/seanlee0923/csms-platform/internal/sessionregistry"
	"github.com/seanlee0923/ocpp/csms"
)

type ownershipEntry struct {
	mu     sync.Mutex
	lease  sessionregistry.Lease
	cancel context.CancelFunc
}

type ownershipManager struct {
	registry      sessionregistry.Registry
	ownerID       string
	ttl           time.Duration
	renewInterval time.Duration
	logger        *slog.Logger
	mu            sync.Mutex
	entries       map[string]*ownershipEntry
}

func newOwnershipManager(registry sessionregistry.Registry, ownerID string, ttl, renewInterval time.Duration, logger *slog.Logger) *ownershipManager {
	return &ownershipManager{
		registry: registry, ownerID: ownerID, ttl: ttl, renewInterval: renewInterval,
		logger: logger, entries: make(map[string]*ownershipEntry),
	}
}

func (manager *ownershipManager) onConnect(session *csms.Session) {
	lease, err := manager.registry.Claim(session.Context(), session.Identity(), manager.ownerID, manager.ttl)
	if err != nil {
		manager.logger.Warn("session ownership claim failed", "identity", session.Identity(), "owner_id", manager.ownerID, "error", err)
		session.Close()
		return
	}
	ctx, cancel := context.WithCancel(session.Context())
	entry := &ownershipEntry{lease: lease, cancel: cancel}
	manager.mu.Lock()
	manager.entries[session.Identity()] = entry
	manager.mu.Unlock()
	manager.logger.Info("session ownership claimed", "identity", session.Identity(), "owner_id", manager.ownerID, "generation", lease.Generation)
	go manager.renew(ctx, session, entry)
}

func (manager *ownershipManager) renew(ctx context.Context, session *csms.Session, entry *ownershipEntry) {
	ticker := time.NewTicker(manager.renewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			entry.mu.Lock()
			renewed, err := manager.registry.Renew(ctx, entry.lease, manager.ttl)
			if err == nil {
				entry.lease = renewed
			}
			entry.mu.Unlock()
			if err != nil {
				manager.logger.Error("session ownership renewal failed", "identity", session.Identity(), "owner_id", manager.ownerID, "error", err)
				session.Close()
				return
			}
		}
	}
}

func (manager *ownershipManager) onDisconnect(session *csms.Session, _ error) {
	manager.mu.Lock()
	entry := manager.entries[session.Identity()]
	delete(manager.entries, session.Identity())
	manager.mu.Unlock()
	if entry == nil {
		return
	}
	entry.cancel()
	entry.mu.Lock()
	lease := entry.lease
	entry.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := manager.registry.Release(ctx, lease); err != nil {
		manager.logger.Warn("session ownership release failed", "identity", session.Identity(), "owner_id", manager.ownerID, "generation", lease.Generation, "error", err)
		return
	}
	manager.logger.Info("session ownership released", "identity", session.Identity(), "owner_id", manager.ownerID, "generation", lease.Generation)
}

func (manager *ownershipManager) owns(identity string, generation uint64) bool {
	manager.mu.Lock()
	entry := manager.entries[identity]
	manager.mu.Unlock()
	if entry == nil {
		return false
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.lease.OwnerID == manager.ownerID && entry.lease.Generation == generation
}
