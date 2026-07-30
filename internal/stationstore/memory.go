package stationstore

import (
	"context"
	"sync"
)

type ConnectorKey struct {
	StationIdentity string
	EVSEID          int
	ConnectorID     int
}

type Memory struct {
	mu         sync.RWMutex
	stations   map[string]Station
	connectors map[ConnectorKey]ConnectorStatus
}

func NewMemory() *Memory {
	return &Memory{
		stations:   make(map[string]Station),
		connectors: make(map[ConnectorKey]ConnectorStatus),
	}
}

func (repository *Memory) UpsertStation(ctx context.Context, station Station) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateStation(station); err != nil {
		return err
	}
	repository.mu.Lock()
	repository.stations[station.Identity] = station
	repository.mu.Unlock()
	return nil
}

func (repository *Memory) UpdateConnectorStatus(ctx context.Context, status ConnectorStatus) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateConnectorStatus(status); err != nil {
		return err
	}
	key := ConnectorKey{StationIdentity: status.StationIdentity, EVSEID: status.EVSEID, ConnectorID: status.ConnectorID}
	repository.mu.Lock()
	repository.connectors[key] = status
	repository.mu.Unlock()
	return nil
}

func (repository *Memory) Station(identity string) (Station, bool) {
	repository.mu.RLock()
	station, ok := repository.stations[identity]
	repository.mu.RUnlock()
	return station, ok
}

func (repository *Memory) Connector(key ConnectorKey) (ConnectorStatus, bool) {
	repository.mu.RLock()
	status, ok := repository.connectors[key]
	repository.mu.RUnlock()
	return status, ok
}
