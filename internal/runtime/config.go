package runtime

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/seanlee0923/ocpp/protocol"
)

const (
	defaultHTTPAddr          = ":8080"
	defaultHeartbeatInterval = 300
	defaultShutdownTimeout   = 30 * time.Second
	defaultSessionLeaseTTL   = 30 * time.Second
	defaultSessionRenew      = 10 * time.Second
)

type Config struct {
	HTTPAddr          string
	HeartbeatInterval int
	ShutdownTimeout   time.Duration
	Versions          []protocol.Version
	LogLevel          slog.Level
	MySQLDSN          string
	RedisURL          string
	APIKeys           []string
	CommandRateLimit  int
	InstanceID        string
	SessionLeaseTTL   time.Duration
	SessionRenew      time.Duration
}

func LoadConfig() (Config, error) {
	return loadConfig(os.LookupEnv)
}

func loadConfig(lookup func(string) (string, bool)) (Config, error) {
	config := Config{
		HTTPAddr: defaultHTTPAddr, HeartbeatInterval: defaultHeartbeatInterval,
		ShutdownTimeout:  defaultShutdownTimeout,
		Versions:         []protocol.Version{protocol.OCPP16, protocol.OCPP201, protocol.OCPP21},
		LogLevel:         slog.LevelInfo,
		SessionLeaseTTL:  defaultSessionLeaseTTL,
		SessionRenew:     defaultSessionRenew,
		CommandRateLimit: 60,
	}
	if value, ok := lookup("CSMS_HTTP_ADDR"); ok {
		if strings.TrimSpace(value) == "" {
			return Config{}, fmt.Errorf("CSMS_HTTP_ADDR must not be empty")
		}
		config.HTTPAddr = value
	}
	if value, ok := lookup("CSMS_HEARTBEAT_INTERVAL"); ok {
		interval, err := strconv.Atoi(value)
		if err != nil || interval <= 0 {
			return Config{}, fmt.Errorf("CSMS_HEARTBEAT_INTERVAL must be a positive integer")
		}
		config.HeartbeatInterval = interval
	}
	if value, ok := lookup("CSMS_SHUTDOWN_TIMEOUT"); ok {
		timeout, err := time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return Config{}, fmt.Errorf("CSMS_SHUTDOWN_TIMEOUT must be a positive duration")
		}
		config.ShutdownTimeout = timeout
	}
	if value, ok := lookup("CSMS_LOG_LEVEL"); ok {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "debug":
			config.LogLevel = slog.LevelDebug
		case "info":
			config.LogLevel = slog.LevelInfo
		case "warn":
			config.LogLevel = slog.LevelWarn
		case "error":
			config.LogLevel = slog.LevelError
		default:
			return Config{}, fmt.Errorf("CSMS_LOG_LEVEL must be one of debug, info, warn, error")
		}
	}
	if value, ok := lookup("CSMS_MYSQL_DSN"); ok {
		config.MySQLDSN = strings.TrimSpace(value)
	}
	if value, ok := lookup("CSMS_REDIS_URL"); ok {
		config.RedisURL = strings.TrimSpace(value)
	}
	if value, ok := lookup("CSMS_API_KEYS"); ok {
		config.APIKeys = splitNonEmpty(value)
	} else if value, ok := lookup("CSMS_API_KEY"); ok && strings.TrimSpace(value) != "" {
		config.APIKeys = []string{strings.TrimSpace(value)}
	}
	if value, ok := lookup("CSMS_COMMAND_RATE_LIMIT"); ok {
		limit, err := strconv.Atoi(value)
		if err != nil || limit <= 0 {
			return Config{}, fmt.Errorf("CSMS_COMMAND_RATE_LIMIT must be a positive integer")
		}
		config.CommandRateLimit = limit
	}
	if value, ok := lookup("CSMS_INSTANCE_ID"); ok {
		config.InstanceID = strings.TrimSpace(value)
	}
	if value, ok := lookup("CSMS_SESSION_LEASE_TTL"); ok {
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			return Config{}, fmt.Errorf("CSMS_SESSION_LEASE_TTL must be a positive duration")
		}
		config.SessionLeaseTTL = duration
	}
	if value, ok := lookup("CSMS_SESSION_RENEW_INTERVAL"); ok {
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			return Config{}, fmt.Errorf("CSMS_SESSION_RENEW_INTERVAL must be a positive duration")
		}
		config.SessionRenew = duration
	}
	if config.SessionRenew >= config.SessionLeaseTTL {
		return Config{}, fmt.Errorf("CSMS_SESSION_RENEW_INTERVAL must be shorter than CSMS_SESSION_LEASE_TTL")
	}
	return config, nil
}

func splitNonEmpty(value string) []string {
	var values []string
	for _, candidate := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}
