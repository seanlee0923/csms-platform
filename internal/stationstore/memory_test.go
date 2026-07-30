package stationstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/seanlee0923/ocpp/protocol"
)

func TestMemoryStoresStationAndConnectorStatus(t *testing.T) {
	repository := NewMemory()
	now := time.Now().UTC()
	station := Station{Identity: "station-1", Version: protocol.OCPP201, Vendor: "vendor", Model: "model", LastBootAt: now}
	if err := repository.UpsertStation(context.Background(), station); err != nil {
		t.Fatal(err)
	}
	status := ConnectorStatus{
		StationIdentity: station.Identity, Version: station.Version, EVSEID: 1, ConnectorID: 2,
		Status: "Available", OccurredAt: now, ReceivedAt: now,
	}
	if err := repository.UpdateConnectorStatus(context.Background(), status); err != nil {
		t.Fatal(err)
	}
	if got, ok := repository.Station(station.Identity); !ok || got != station {
		t.Fatalf("station = %+v, %v", got, ok)
	}
	key := ConnectorKey{StationIdentity: station.Identity, EVSEID: 1, ConnectorID: 2}
	if got, ok := repository.Connector(key); !ok || got != status {
		t.Fatalf("connector = %+v, %v", got, ok)
	}
}

func TestMemoryRejectsInvalidAndCanceledWrites(t *testing.T) {
	repository := NewMemory()
	if err := repository.UpsertStation(context.Background(), Station{}); !errors.Is(err, ErrInvalidStation) {
		t.Fatalf("got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := repository.UpdateConnectorStatus(ctx, ConnectorStatus{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestMemorySupportsConcurrentWrites(t *testing.T) {
	repository := NewMemory()
	var wait sync.WaitGroup
	for range 100 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			err := repository.UpsertStation(context.Background(), Station{
				Identity: "station", Version: protocol.OCPP16, Vendor: "vendor", Model: "model", LastBootAt: time.Now().UTC(),
			})
			if err != nil {
				t.Errorf("write: %v", err)
			}
		}()
	}
	wait.Wait()
}
