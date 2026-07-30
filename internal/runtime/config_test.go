package runtime

import (
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	config, err := loadConfig(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if config.HTTPAddr != ":8080" || config.HeartbeatInterval != 300 || config.ShutdownTimeout != 30*time.Second || len(config.Versions) != 3 || config.LogLevel != 0 {
		t.Fatalf("unexpected defaults: %+v", config)
	}
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	tests := map[string]string{
		"CSMS_HTTP_ADDR": "", "CSMS_HEARTBEAT_INTERVAL": "0", "CSMS_SHUTDOWN_TIMEOUT": "soon", "CSMS_LOG_LEVEL": "verbose",
		"CSMS_SESSION_LEASE_TTL": "0s", "CSMS_SESSION_RENEW_INTERVAL": "never",
		"CSMS_COMMAND_RATE_LIMIT": "0",
	}
	for key, value := range tests {
		t.Run(key, func(t *testing.T) {
			_, err := loadConfig(func(candidate string) (string, bool) {
				return value, candidate == key
			})
			if err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestLoadConfigSupportsCredentialRotation(t *testing.T) {
	config, err := loadConfig(func(key string) (string, bool) {
		if key == "CSMS_API_KEYS" {
			return "old-key, new-key", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(config.APIKeys) != 2 || config.APIKeys[0] != "old-key" || config.APIKeys[1] != "new-key" {
		t.Fatalf("unexpected API keys: %#v", config.APIKeys)
	}
}

func TestLoadConfigRejectsRenewIntervalAtOrAboveTTL(t *testing.T) {
	values := map[string]string{
		"CSMS_SESSION_LEASE_TTL":      "10s",
		"CSMS_SESSION_RENEW_INTERVAL": "10s",
	}
	_, err := loadConfig(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestLoadConfigLogLevels(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error", "WARN"} {
		t.Run(level, func(t *testing.T) {
			config, err := loadConfig(func(key string) (string, bool) {
				return level, key == "CSMS_LOG_LEVEL"
			})
			if err != nil {
				t.Fatal(err)
			}
			if level == "debug" && config.LogLevel >= 0 {
				t.Fatalf("debug level not applied: %v", config.LogLevel)
			}
		})
	}
}
