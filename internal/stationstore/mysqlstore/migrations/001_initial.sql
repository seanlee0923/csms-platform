CREATE TABLE IF NOT EXISTS stations (
    identity VARCHAR(255) PRIMARY KEY,
    ocpp_version VARCHAR(16) NOT NULL,
    vendor VARCHAR(50) NOT NULL,
    model VARCHAR(50) NOT NULL,
    serial_number VARCHAR(50) NOT NULL DEFAULT '',
    firmware_version VARCHAR(50) NOT NULL DEFAULT '',
    boot_reason VARCHAR(32) NOT NULL DEFAULT '',
    last_boot_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)
);

CREATE TABLE IF NOT EXISTS connector_status (
    station_identity VARCHAR(255) NOT NULL,
    evse_id INT NOT NULL,
    connector_id INT NOT NULL,
    ocpp_version VARCHAR(16) NOT NULL,
    status VARCHAR(32) NOT NULL,
    error_code VARCHAR(64) NOT NULL DEFAULT '',
    occurred_at DATETIME(6) NOT NULL,
    received_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (station_identity, evse_id, connector_id),
    CONSTRAINT fk_connector_status_station
        FOREIGN KEY (station_identity) REFERENCES stations(identity) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS boot_events (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    station_identity VARCHAR(255) NOT NULL,
    ocpp_version VARCHAR(16) NOT NULL,
    vendor VARCHAR(50) NOT NULL,
    model VARCHAR(50) NOT NULL,
    serial_number VARCHAR(50) NOT NULL DEFAULT '',
    firmware_version VARCHAR(50) NOT NULL DEFAULT '',
    boot_reason VARCHAR(32) NOT NULL DEFAULT '',
    occurred_at DATETIME(6) NOT NULL,
    INDEX idx_boot_events_station_time (station_identity, occurred_at),
    CONSTRAINT fk_boot_events_station
        FOREIGN KEY (station_identity) REFERENCES stations(identity) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS status_events (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    station_identity VARCHAR(255) NOT NULL,
    evse_id INT NOT NULL,
    connector_id INT NOT NULL,
    ocpp_version VARCHAR(16) NOT NULL,
    status VARCHAR(32) NOT NULL,
    error_code VARCHAR(64) NOT NULL DEFAULT '',
    occurred_at DATETIME(6) NOT NULL,
    received_at DATETIME(6) NOT NULL,
    INDEX idx_status_events_station_connector_time
        (station_identity, evse_id, connector_id, occurred_at),
    CONSTRAINT fk_status_events_station
        FOREIGN KEY (station_identity) REFERENCES stations(identity) ON DELETE CASCADE
);
