package mysqlstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/seanlee0923/csms-platform/internal/stationstore"
)

type Repository struct {
	database *sql.DB
}

func New(database *sql.DB) (*Repository, error) {
	if database == nil {
		return nil, fmt.Errorf("mysql database is nil")
	}
	return &Repository{database: database}, nil
}

func (repository *Repository) UpsertStation(ctx context.Context, station stationstore.Station) error {
	if err := stationstore.ValidateStation(station); err != nil {
		return err
	}
	transaction, err := repository.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin station transaction: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO stations (
			identity, ocpp_version, vendor, model, serial_number, firmware_version, boot_reason, last_boot_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			ocpp_version = VALUES(ocpp_version),
			vendor = VALUES(vendor),
			model = VALUES(model),
			serial_number = VALUES(serial_number),
			firmware_version = VALUES(firmware_version),
			boot_reason = VALUES(boot_reason),
			last_boot_at = VALUES(last_boot_at)`,
		station.Identity, station.Version, station.Vendor, station.Model, station.SerialNumber,
		station.FirmwareVersion, station.BootReason, station.LastBootAt,
	); err != nil {
		return fmt.Errorf("upsert station: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO boot_events (
			station_identity, ocpp_version, vendor, model, serial_number, firmware_version, boot_reason, occurred_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		station.Identity, station.Version, station.Vendor, station.Model, station.SerialNumber,
		station.FirmwareVersion, station.BootReason, station.LastBootAt,
	); err != nil {
		return fmt.Errorf("insert boot event: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit station transaction: %w", err)
	}
	return nil
}

func (repository *Repository) UpdateConnectorStatus(ctx context.Context, status stationstore.ConnectorStatus) error {
	if err := stationstore.ValidateConnectorStatus(status); err != nil {
		return err
	}
	transaction, err := repository.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin connector status transaction: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO connector_status (
			station_identity, evse_id, connector_id, ocpp_version, status, error_code, occurred_at, received_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			ocpp_version = VALUES(ocpp_version),
			status = VALUES(status),
			error_code = VALUES(error_code),
			occurred_at = VALUES(occurred_at),
			received_at = VALUES(received_at)`,
		status.StationIdentity, status.EVSEID, status.ConnectorID, status.Version,
		status.Status, status.ErrorCode, status.OccurredAt, status.ReceivedAt,
	); err != nil {
		return fmt.Errorf("upsert connector status: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO status_events (
			station_identity, evse_id, connector_id, ocpp_version, status, error_code, occurred_at, received_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		status.StationIdentity, status.EVSEID, status.ConnectorID, status.Version,
		status.Status, status.ErrorCode, status.OccurredAt, status.ReceivedAt,
	); err != nil {
		return fmt.Errorf("insert status event: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit connector status transaction: %w", err)
	}
	return nil
}
