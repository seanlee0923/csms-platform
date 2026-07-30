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
		"CSMS_COMMAND_RATE_LIMIT": "0", "CSMS_MYSQL_MAX_OPEN_CONNS": "0",
		"CSMS_TRUSTED_PROXY_CIDRS": "not-a-cidr",
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

func TestLoadConfigMySQLMaxOpenConns(t *testing.T) {
	defaultConfig, err := loadConfig(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if defaultConfig.MySQLMaxOpenConns != 25 {
		t.Fatalf("expected default MySQLMaxOpenConns=25, got %d", defaultConfig.MySQLMaxOpenConns)
	}

	overridden, err := loadConfig(func(key string) (string, bool) {
		return "5", key == "CSMS_MYSQL_MAX_OPEN_CONNS"
	})
	if err != nil {
		t.Fatal(err)
	}
	if overridden.MySQLMaxOpenConns != 5 {
		t.Fatalf("expected overridden MySQLMaxOpenConns=5, got %d", overridden.MySQLMaxOpenConns)
	}
}

func TestLoadConfigTrustedProxyCIDRs(t *testing.T) {
	defaultConfig, err := loadConfig(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultConfig.TrustedProxyCIDRs) != 0 {
		t.Fatalf("expected no trusted proxy CIDRs by default, got %v", defaultConfig.TrustedProxyCIDRs)
	}

	overridden, err := loadConfig(func(key string) (string, bool) {
		return "10.0.0.0/8, 192.168.0.0/16", key == "CSMS_TRUSTED_PROXY_CIDRS"
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.0/8", "192.168.0.0/16"}
	if len(overridden.TrustedProxyCIDRs) != len(want) || overridden.TrustedProxyCIDRs[0] != want[0] || overridden.TrustedProxyCIDRs[1] != want[1] {
		t.Fatalf("TrustedProxyCIDRs = %v, want %v", overridden.TrustedProxyCIDRs, want)
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
