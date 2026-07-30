package runtime

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/seanlee0923/csms-platform/internal/commandbus"
	"github.com/seanlee0923/csms-platform/internal/sessionregistry"
)

func TestHandleCommandRejectsStaleOwnerGeneration(t *testing.T) {
	registry := sessionregistry.NewMemory()
	lease, err := registry.Claim(context.Background(), "station-1", "runtime-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	manager := newOwnershipManager(registry, "runtime-a", time.Minute, 10*time.Second, slog.Default())
	manager.entries["station-1"] = &ownershipEntry{lease: lease}
	server := &Server{ownership: manager}

	result := server.handleCommand(context.Background(), commandbus.Command{
		StationIdentity: "station-1", OwnerID: "runtime-a", OwnerGeneration: lease.Generation + 1,
	})

	if result.Success || result.ErrorCode != "NotOwner" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestHandleCommandRejectsExpiredDeadline(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errors.New("deadline passed"))
	server := &Server{}

	result := server.handleCommand(ctx, commandbus.Command{})

	if result.Success || result.ErrorCode != "Expired" {
		t.Fatalf("unexpected result: %+v", result)
	}
}
