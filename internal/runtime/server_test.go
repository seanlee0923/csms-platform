package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/websocket"
	"github.com/seanlee0923/csms-platform/internal/stationstore"
	"github.com/seanlee0923/ocpp/protocol"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	server, err := New(Config{
		HTTPAddr: ":0", HeartbeatInterval: 123, ShutdownTimeout: time.Second,
		Versions: []protocol.Version{protocol.OCPP16, protocol.OCPP201, protocol.OCPP21},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func TestHealthEndpoints(t *testing.T) {
	server := newTestServer(t)
	for _, test := range []struct {
		path string
		want int
	}{{"/livez", http.StatusOK}, {"/readyz", http.StatusServiceUnavailable}} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("%s: got %d, want %d", test.path, response.Code, test.want)
		}
	}
	server.health.ready.Store(true)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("ready: got %d", response.Code)
	}
}

func TestOCPPEndpointRejectsPlainHTTPAndUnsupportedProtocol(t *testing.T) {
	testServer := httptest.NewServer(newTestServer(t).Handler())
	defer testServer.Close()
	response, err := http.Get(testServer.URL + "/station")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("plain HTTP: got %d", response.StatusCode)
	}
	_, response, err = websocket.DefaultDialer.Dial(wsURL(testServer.URL)+"/station", http.Header{
		"Sec-Websocket-Protocol": []string{"unsupported"},
	})
	if err == nil {
		t.Fatal("unsupported protocol unexpectedly connected")
	}
	if response == nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsupported protocol response: %#v", response)
	}
}

// TestHandshakeRateLimitProtectsAgainstReconnectStorms proves the fix for a
// real incident found under load testing: a reconnect storm (many stations
// retrying immediately after any rejection, with no backoff) is
// self-sustaining without a limit on connection attempts, because each
// failure keeps downstream stores busy enough to cause the next one. A
// per-IP HandshakeLimiter turns excess attempts into a fast 429 instead of
// letting them all reach the OCPP handler.
func TestHandshakeRateLimitProtectsAgainstReconnectStorms(t *testing.T) {
	server, err := New(Config{
		HTTPAddr: ":0", HeartbeatInterval: 123, ShutdownTimeout: time.Second,
		Versions:           []protocol.Version{protocol.OCPP16},
		HandshakeRateLimit: 3,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

	dialer := websocket.Dialer{Subprotocols: []string{"ocpp1.6"}}
	allowed, limited := 0, 0
	for i := 0; i < 6; i++ {
		conn, resp, err := dialer.Dial(wsURL(testServer.URL)+fmt.Sprintf("/storm-station-%d", i), nil)
		if err == nil {
			allowed++
			conn.Close()
			continue
		}
		if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
			limited++
			continue
		}
		t.Fatalf("attempt %d: unexpected failure (resp=%#v): %v", i, resp, err)
	}
	if allowed != 3 {
		t.Errorf("expected exactly 3 handshakes allowed within the limit, got %d", allowed)
	}
	if limited != 3 {
		t.Errorf("expected exactly 3 handshakes rejected with 429 once the limit is exceeded, got %d", limited)
	}
}

func TestCoreFlowAllVersions(t *testing.T) {
	runtimeServer := newTestServer(t)
	testServer := httptest.NewServer(runtimeServer.Handler())
	defer testServer.Close()
	tests := []struct {
		version string
		boot    any
		status  any
		evseID  int
	}{
		{"ocpp1.6", map[string]any{"chargePointVendor": "vendor", "chargePointModel": "model"}, map[string]any{"connectorId": 1, "errorCode": "NoError", "status": "Available"}, 0},
		{"ocpp2.0.1", map[string]any{"reason": "PowerUp", "chargingStation": map[string]any{"vendorName": "vendor", "model": "model"}}, map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339), "connectorStatus": "Available", "evseId": 1, "connectorId": 1}, 1},
		{"ocpp2.1", map[string]any{"reason": "PowerUp", "chargingStation": map[string]any{"vendorName": "vendor", "model": "model"}}, map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339), "connectorStatus": "Available", "evseId": 1, "connectorId": 1}, 1},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			dialer := websocket.Dialer{Subprotocols: []string{test.version}}
			identity := "station-" + strings.ReplaceAll(test.version, ".", "-")
			connection, _, err := dialer.Dial(wsURL(testServer.URL)+"/"+identity, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			boot := call(t, connection, "1", "BootNotification", test.boot)
			if boot["status"] != "Accepted" || boot["interval"] != float64(123) {
				t.Fatalf("unexpected boot response: %#v", boot)
			}
			if _, err := time.Parse(time.RFC3339, boot["currentTime"].(string)); err != nil {
				t.Fatalf("invalid boot time: %v", err)
			}
			heartbeat := call(t, connection, "2", "Heartbeat", map[string]any{})
			if _, err := time.Parse(time.RFC3339, heartbeat["currentTime"].(string)); err != nil {
				t.Fatalf("invalid heartbeat time: %v", err)
			}
			status := call(t, connection, "3", "StatusNotification", test.status)
			if len(status) != 0 {
				t.Fatalf("unexpected status response: %#v", status)
			}
			if station, ok := runtimeServer.store.Station(identity); !ok || station.Vendor != "vendor" || station.Model != "model" {
				t.Fatalf("station was not persisted: %+v, %v", station, ok)
			}
			key := stationstore.ConnectorKey{StationIdentity: identity, EVSEID: test.evseID, ConnectorID: 1}
			if connector, ok := runtimeServer.store.Connector(key); !ok || connector.Status != "Available" {
				t.Fatalf("connector status was not persisted: %+v, %v", connector, ok)
			}
		})
	}
}

func TestContextCancellationStopsAcceptingConnections(t *testing.T) {
	server := newTestServer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- server.serve(ctx, listener) }()

	baseURL := "http://" + listener.Addr().String()
	waitForStatus(t, baseURL+"/readyz", http.StatusOK)
	dialer := websocket.Dialer{Subprotocols: []string{"ocpp1.6"}}
	connection, _, err := dialer.Dial(wsURL(baseURL)+"/shutdown-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("shutdown failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runtime did not stop")
	}
	if server.health.ready.Load() {
		t.Fatal("runtime remained ready after shutdown")
	}
	connection.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := connection.ReadMessage(); err == nil {
		t.Fatal("existing WebSocket remained open")
	}
	connection.Close()
	if _, err := net.DialTimeout("tcp", listener.Addr().String(), 200*time.Millisecond); err == nil {
		t.Fatal("runtime still accepts new connections")
	}
}

func TestRunReturnsListenError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := newTestServer(t)
	server.config.HTTPAddr = listener.Addr().String()
	if err := server.Run(context.Background()); err == nil {
		t.Fatal("expected listen error for address already in use")
	}
	if server.health.ready.Load() {
		t.Fatal("runtime became ready after listen failure")
	}
}

func TestShutdownTimeoutBoundsShutdown(t *testing.T) {
	server := newTestServer(t)
	server.config.ShutdownTimeout = 50 * time.Millisecond
	server.shutdownOCPP = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	server.shutdownHTTP = func(context.Context) error { return nil }
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- server.serve(ctx, listener) }()
	waitForStatus(t, "http://"+listener.Addr().String()+"/readyz", http.StatusOK)

	started := time.Now()
	cancel()
	err = <-errCh
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want deadline exceeded", err)
	}
	if elapsed < server.config.ShutdownTimeout || elapsed > 500*time.Millisecond {
		t.Fatalf("shutdown took %s with timeout %s", elapsed, server.config.ShutdownTimeout)
	}
}

func TestRedisOwnershipRejectsAnotherRuntimeAndReleasesOnDisconnect(t *testing.T) {
	redisServer := miniredis.RunT(t)
	config := Config{
		HTTPAddr: ":0", HeartbeatInterval: 123, ShutdownTimeout: time.Second,
		Versions: []protocol.Version{protocol.OCPP16, protocol.OCPP201, protocol.OCPP21},
		RedisURL: "redis://" + redisServer.Addr(), InstanceID: "runtime-a",
		SessionLeaseTTL: 30 * time.Second, SessionRenew: 10 * time.Second,
	}
	firstRuntime, err := New(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	firstServer := httptest.NewServer(firstRuntime.Handler())
	defer firstServer.Close()
	dialer := websocket.Dialer{Subprotocols: []string{"ocpp1.6"}}
	firstConnection, _, err := dialer.Dial(wsURL(firstServer.URL)+"/owned-station", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer firstConnection.Close()
	waitForRedisOwner(t, redisServer, "csms:session:owned-station", "runtime-a")

	config.InstanceID = "runtime-b"
	secondRuntime, err := New(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	secondServer := httptest.NewServer(secondRuntime.Handler())
	defer secondServer.Close()
	secondConnection, _, err := dialer.Dial(wsURL(secondServer.URL)+"/owned-station", nil)
	if err == nil {
		secondConnection.SetReadDeadline(time.Now().Add(time.Second))
		if _, _, readErr := secondConnection.ReadMessage(); readErr == nil {
			t.Fatal("conflicting runtime kept the WebSocket open")
		}
		secondConnection.Close()
	}
	waitForRedisOwner(t, redisServer, "csms:session:owned-station", "runtime-a")

	firstConnection.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !redisServer.Exists("csms:session:owned-station") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("ownership was not released after disconnect")
}

func waitForRedisOwner(t *testing.T, server *miniredis.Miniredis, key, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if server.Exists(key) && server.HGet(key, "owner") == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s owner did not become %s", key, want)
}

func waitForStatus(t *testing.T, url string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(url)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not return %d", url, want)
}

func call(t *testing.T, connection *websocket.Conn, id, action string, payload any) map[string]any {
	t.Helper()
	if err := connection.WriteJSON([]any{2, id, action, payload}); err != nil {
		t.Fatal(err)
	}
	var response []json.RawMessage
	if err := connection.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	if len(response) != 3 || string(response[0]) != "3" {
		t.Fatalf("unexpected OCPP response: %s", response)
	}
	var result map[string]any
	if err := json.Unmarshal(response[2], &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func wsURL(httpURL string) string { return "ws" + strings.TrimPrefix(httpURL, "http") }
