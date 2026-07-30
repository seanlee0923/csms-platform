package mysqlstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/seanlee0923/csms-platform/internal/stationstore"
	"github.com/seanlee0923/ocpp/protocol"
)

func TestUpsertStationUpdatesCurrentStateAndAppendsEvent(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	station := stationstore.Station{
		Identity: "station-1", Version: protocol.OCPP201, Vendor: "vendor", Model: "model",
		SerialNumber: "serial", FirmwareVersion: "1.0", BootReason: "PowerUp", LastBootAt: time.Now().UTC(),
	}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO stations").WithArgs(
		station.Identity, station.Version, station.Vendor, station.Model, station.SerialNumber,
		station.FirmwareVersion, station.BootReason, station.LastBootAt,
	).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO boot_events").WithArgs(
		station.Identity, station.Version, station.Vendor, station.Model, station.SerialNumber,
		station.FirmwareVersion, station.BootReason, station.LastBootAt,
	).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := repository.UpsertStation(context.Background(), station); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConnectorEventFailureRollsBackCurrentState(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	status := stationstore.ConnectorStatus{
		StationIdentity: "station-1", Version: protocol.OCPP16, ConnectorID: 1,
		Status: "Available", ErrorCode: "NoError", OccurredAt: now, ReceivedAt: now,
	}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO connector_status").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO status_events").WillReturnError(errors.New("write failed"))
	mock.ExpectRollback()

	if err := repository.UpdateConnectorStatus(context.Background(), status); err == nil {
		t.Fatal("expected an error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateExecutesSchemaStatements(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for range 4 {
		mock.ExpectExec("CREATE TABLE IF NOT EXISTS").WillReturnResult(sqlmock.NewResult(0, 0))
	}
	if err := Migrate(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
