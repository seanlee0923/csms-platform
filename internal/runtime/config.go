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

	// defaultMySQLMaxOpenConns bounds how many concurrent MySQL connections
	// this Runtime process will open. database/sql defaults to unlimited,
	// which lets a burst of simultaneous BootNotification/StatusNotification
	// writes (a reconnect storm after a network blip or a Pod restart) open
	// far more MySQL connections than the server can actually handle at
	// once; excess requests then fail outright instead of queuing for a
	// pooled connection. A bounded pool turns that failure mode into
	// backpressure instead.
	defaultMySQLMaxOpenConns    = 25
	defaultMySQLConnMaxLifetime = 5 * time.Minute

	// defaultHandshakeRateLimit bounds how many WebSocket upgrade attempts
	// per remote IP per minute the Runtime accepts, using the ocpp
	// library's built-in HandshakeLimiter. Without it, a reconnect storm
	// (many stations retrying immediately after any failure, with no
	// backoff) is self-sustaining: each rejected attempt is immediately
	// followed by another, keeping downstream stores like MySQL saturated
	// indefinitely instead of letting them recover. This is independent of
	// CSMS_COMMAND_RATE_LIMIT, which only governs the outbound command API.
	defaultHandshakeRateLimit = 30

	// These default paths match the volume mount convention the Operator
	// uses when CSMS.spec.tls is set (internal/controller's tlsServerMountPath/
	// tlsCAMountPath). TLS activates automatically if a file exists at
	// TLSCertFile/TLSKeyFile at startup — there is no separate on/off flag.
	defaultTLSCertFile     = "/etc/csms/tls/server/tls.crt"
	defaultTLSKeyFile      = "/etc/csms/tls/server/tls.key"
	defaultTLSClientCAFile = "/etc/csms/tls/ca/ca.crt"
)

type Config struct {
	HTTPAddr           string
	HeartbeatInterval  int
	ShutdownTimeout    time.Duration
	Versions           []protocol.Version
	LogLevel           slog.Level
	MySQLDSN           string
	MySQLMaxOpenConns  int
	HandshakeRateLimit int
	RedisURL           string
	APIKeys            []string
	CommandRateLimit   int
	InstanceID         string
	SessionLeaseTTL    time.Duration
	SessionRenew       time.Duration
	TLSCertFile        string
	TLSKeyFile         string
	TLSClientCAFile    string
}

func LoadConfig() (Config, error) {
	return loadConfig(os.LookupEnv)
}

func loadConfig(lookup func(string) (string, bool)) (Config, error) {
	config := Config{
		HTTPAddr: defaultHTTPAddr, HeartbeatInterval: defaultHeartbeatInterval,
		ShutdownTimeout:    defaultShutdownTimeout,
		Versions:           []protocol.Version{protocol.OCPP16, protocol.OCPP201, protocol.OCPP21},
		LogLevel:           slog.LevelInfo,
		SessionLeaseTTL:    defaultSessionLeaseTTL,
		SessionRenew:       defaultSessionRenew,
		CommandRateLimit:   60,
		MySQLMaxOpenConns:  defaultMySQLMaxOpenConns,
		HandshakeRateLimit: defaultHandshakeRateLimit,
		TLSCertFile:        defaultTLSCertFile,
		TLSKeyFile:         defaultTLSKeyFile,
		TLSClientCAFile:    defaultTLSClientCAFile,
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
	if value, ok := lookup("CSMS_MYSQL_MAX_OPEN_CONNS"); ok {
		limit, err := strconv.Atoi(value)
		if err != nil || limit <= 0 {
			return Config{}, fmt.Errorf("CSMS_MYSQL_MAX_OPEN_CONNS must be a positive integer")
		}
		config.MySQLMaxOpenConns = limit
	}
	if value, ok := lookup("CSMS_HANDSHAKE_RATE_LIMIT"); ok {
		limit, err := strconv.Atoi(value)
		if err != nil || limit <= 0 {
			return Config{}, fmt.Errorf("CSMS_HANDSHAKE_RATE_LIMIT must be a positive integer")
		}
		config.HandshakeRateLimit = limit
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
	if value, ok := lookup("CSMS_TLS_CERT_FILE"); ok {
		config.TLSCertFile = strings.TrimSpace(value)
	}
	if value, ok := lookup("CSMS_TLS_KEY_FILE"); ok {
		config.TLSKeyFile = strings.TrimSpace(value)
	}
	if value, ok := lookup("CSMS_TLS_CLIENT_CA_FILE"); ok {
		config.TLSClientCAFile = strings.TrimSpace(value)
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
