package stationstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/seanlee0923/ocpp/protocol"
)

var (
	ErrInvalidStation   = errors.New("invalid station")
	ErrInvalidConnector = errors.New("invalid connector status")
)

type Station struct {
	Identity        string
	Version         protocol.Version
	Vendor          string
	Model           string
	SerialNumber    string
	FirmwareVersion string
	BootReason      string
	LastBootAt      time.Time
}

type ConnectorStatus struct {
	StationIdentity string
	Version         protocol.Version
	EVSEID          int
	ConnectorID     int
	Status          string
	ErrorCode       string
	OccurredAt      time.Time
	ReceivedAt      time.Time
}

type Repository interface {
	UpsertStation(context.Context, Station) error
	UpdateConnectorStatus(context.Context, ConnectorStatus) error
}

// ValidateStation reports whether station has every field a Repository
// adapter requires before persisting it.
func ValidateStation(station Station) error {
	if station.Identity == "" || !station.Version.Valid() || station.Vendor == "" || station.Model == "" || station.LastBootAt.IsZero() {
		return fmt.Errorf("%w: identity, version, vendor, model and boot time are required", ErrInvalidStation)
	}
	return nil
}

// ValidateConnectorStatus reports whether status has every field a
// Repository adapter requires before persisting it.
func ValidateConnectorStatus(status ConnectorStatus) error {
	if status.StationIdentity == "" || !status.Version.Valid() || status.EVSEID < 0 || status.ConnectorID < 0 || status.Status == "" || status.ReceivedAt.IsZero() {
		return fmt.Errorf("%w: identity, version, non-negative connector identifiers, status and receive time are required", ErrInvalidConnector)
	}
	return nil
}
