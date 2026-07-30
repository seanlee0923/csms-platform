package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/seanlee0923/csms-platform/internal/stationstore"
	"github.com/seanlee0923/ocpp/csms"
	"github.com/seanlee0923/ocpp/profiles/ocpp16"
	"github.com/seanlee0923/ocpp/profiles/ocpp201"
	"github.com/seanlee0923/ocpp/profiles/ocpp21"
	"github.com/seanlee0923/ocpp/v16"
	"github.com/seanlee0923/ocpp/v201"
	"github.com/seanlee0923/ocpp/v21"
)

type Profiles struct {
	OCPP16  *ocpp16.Profile
	OCPP201 *ocpp201.Profile
	OCPP21  *ocpp21.Profile
}

func Register(router *csms.Router, interval int, logger *slog.Logger, repository stationstore.Repository) (*Profiles, error) {
	if router == nil {
		return nil, fmt.Errorf("router is nil")
	}
	if repository == nil {
		return nil, fmt.Errorf("station repository is nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	p16, err := ocpp16.NewProfile(router)
	if err != nil {
		return nil, err
	}
	p201, err := ocpp201.NewProfile(router)
	if err != nil {
		return nil, err
	}
	p21, err := ocpp21.NewProfile(router)
	if err != nil {
		return nil, err
	}
	if err := p16.HandleBootNotification(func(ctx context.Context, session *csms.Session, request v16.BootNotificationRequest) (v16.BootNotificationConfirmation, error) {
		bootedAt := time.Now().UTC()
		err := repository.UpsertStation(ctx, stationstore.Station{
			Identity: session.Identity(), Version: session.Version(), Vendor: request.ChargePointVendor,
			Model: request.ChargePointModel, SerialNumber: firstString(request.ChargePointSerialNumber, request.ChargeBoxSerialNumber),
			FirmwareVersion: value(request.FirmwareVersion), LastBootAt: bootedAt,
		})
		return v16.BootNotificationConfirmation{CurrentTime: formatTime(bootedAt), Interval: interval, Status: v16.BootNotificationConfirmationStatusAccepted}, err
	}); err != nil {
		return nil, err
	}
	if err := p201.HandleBootNotification(func(ctx context.Context, session *csms.Session, request v201.BootNotificationRequest) (v201.BootNotificationConfirmation, error) {
		bootedAt := time.Now().UTC()
		err := repository.UpsertStation(ctx, stationstore.Station{
			Identity: session.Identity(), Version: session.Version(), Vendor: request.ChargingStation.VendorName,
			Model: request.ChargingStation.Model, SerialNumber: value(request.ChargingStation.SerialNumber),
			FirmwareVersion: value(request.ChargingStation.FirmwareVersion), BootReason: string(request.Reason), LastBootAt: bootedAt,
		})
		return v201.BootNotificationConfirmation{CurrentTime: formatTime(bootedAt), Interval: interval, Status: v201.BootNotificationConfirmationRegistrationStatusEnumAccepted}, err
	}); err != nil {
		return nil, err
	}
	if err := p21.HandleBootNotification(func(ctx context.Context, session *csms.Session, request v21.BootNotificationRequest) (v21.BootNotificationConfirmation, error) {
		bootedAt := time.Now().UTC()
		err := repository.UpsertStation(ctx, stationstore.Station{
			Identity: session.Identity(), Version: session.Version(), Vendor: request.ChargingStation.VendorName,
			Model: request.ChargingStation.Model, SerialNumber: value(request.ChargingStation.SerialNumber),
			FirmwareVersion: value(request.ChargingStation.FirmwareVersion), BootReason: string(request.Reason), LastBootAt: bootedAt,
		})
		return v21.BootNotificationConfirmation{CurrentTime: formatTime(bootedAt), Interval: interval, Status: v21.BootNotificationConfirmationRegistrationStatusEnumAccepted}, err
	}); err != nil {
		return nil, err
	}
	if err := p16.HandleHeartbeat(func(context.Context, *csms.Session, v16.HeartbeatRequest) (v16.HeartbeatConfirmation, error) {
		return v16.HeartbeatConfirmation{CurrentTime: now()}, nil
	}); err != nil {
		return nil, err
	}
	if err := p201.HandleHeartbeat(func(context.Context, *csms.Session, v201.HeartbeatRequest) (v201.HeartbeatConfirmation, error) {
		return v201.HeartbeatConfirmation{CurrentTime: now()}, nil
	}); err != nil {
		return nil, err
	}
	if err := p21.HandleHeartbeat(func(context.Context, *csms.Session, v21.HeartbeatRequest) (v21.HeartbeatConfirmation, error) {
		return v21.HeartbeatConfirmation{CurrentTime: now()}, nil
	}); err != nil {
		return nil, err
	}
	if err := p16.HandleStatusNotification(func(ctx context.Context, session *csms.Session, request v16.StatusNotificationRequest) (v16.StatusNotificationConfirmation, error) {
		receivedAt := time.Now().UTC()
		err := repository.UpdateConnectorStatus(ctx, stationstore.ConnectorStatus{
			StationIdentity: session.Identity(), Version: session.Version(), ConnectorID: request.ConnectorID,
			Status: string(request.Status), ErrorCode: string(request.ErrorCode),
			OccurredAt: optionalTime(request.Timestamp, receivedAt), ReceivedAt: receivedAt,
		})
		logger.Info("status notification", "identity", session.Identity(), "version", session.Version(), "connector_id", request.ConnectorID, "status", request.Status, "error_code", request.ErrorCode)
		return v16.StatusNotificationConfirmation{}, err
	}); err != nil {
		return nil, err
	}
	if err := p201.HandleStatusNotification(func(ctx context.Context, session *csms.Session, request v201.StatusNotificationRequest) (v201.StatusNotificationConfirmation, error) {
		receivedAt := time.Now().UTC()
		err := repository.UpdateConnectorStatus(ctx, stationstore.ConnectorStatus{
			StationIdentity: session.Identity(), Version: session.Version(), EVSEID: request.EVSEID, ConnectorID: request.ConnectorID,
			Status: string(request.ConnectorStatus), OccurredAt: requiredTime(request.Timestamp, receivedAt), ReceivedAt: receivedAt,
		})
		logger.Info("status notification", "identity", session.Identity(), "version", session.Version(), "evse_id", request.EVSEID, "connector_id", request.ConnectorID, "status", request.ConnectorStatus)
		return v201.StatusNotificationConfirmation{}, err
	}); err != nil {
		return nil, err
	}
	if err := p21.HandleStatusNotification(func(ctx context.Context, session *csms.Session, request v21.StatusNotificationRequest) (v21.StatusNotificationConfirmation, error) {
		receivedAt := time.Now().UTC()
		err := repository.UpdateConnectorStatus(ctx, stationstore.ConnectorStatus{
			StationIdentity: session.Identity(), Version: session.Version(), EVSEID: request.EVSEID, ConnectorID: request.ConnectorID,
			Status: string(request.ConnectorStatus), OccurredAt: requiredTime(request.Timestamp, receivedAt), ReceivedAt: receivedAt,
		})
		logger.Info("status notification", "identity", session.Identity(), "version", session.Version(), "evse_id", request.EVSEID, "connector_id", request.ConnectorID, "status", request.ConnectorStatus)
		return v21.StatusNotificationConfirmation{}, err
	}); err != nil {
		return nil, err
	}
	return &Profiles{OCPP16: p16, OCPP201: p201, OCPP21: p21}, nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func formatTime(value time.Time) string { return value.Format(time.RFC3339) }

func value(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}

func firstString(values ...*string) string {
	for _, candidate := range values {
		if candidate != nil && *candidate != "" {
			return *candidate
		}
	}
	return ""
}

func optionalTime(value *string, fallback time.Time) time.Time {
	if value == nil {
		return fallback
	}
	return requiredTime(*value, fallback)
}

func requiredTime(value string, fallback time.Time) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return fallback
	}
	return parsed.UTC()
}
