package runtime

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seanlee0923/csms-platform/internal/commandbus"
	"github.com/seanlee0923/csms-platform/internal/sessionregistry"
)

type commandBusStub struct {
	published commandbus.Command
	result    commandbus.Result
}

type rateLimiterStub struct {
	allowed bool
	err     error
}

func (stub rateLimiterStub) Allow(context.Context, string) (bool, error) {
	return stub.allowed, stub.err
}

func (stub *commandBusStub) Publish(_ context.Context, command commandbus.Command) error {
	stub.published = command
	return nil
}

func (stub *commandBusStub) AwaitResult(context.Context, string) (commandbus.Result, error) {
	return stub.result, nil
}

func (*commandBusStub) RunConsumer(context.Context, string, commandbus.Handler) error { return nil }

func TestResetCommandAPIRoutesUsingCurrentLease(t *testing.T) {
	registry := sessionregistry.NewMemory()
	lease, err := registry.Claim(context.Background(), "station-1", "runtime-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ownership := newOwnershipManager(registry, "runtime-a", time.Minute, 10*time.Second, slog.Default())
	bus := &commandBusStub{result: commandbus.Result{CommandID: "result", Success: true, Payload: json.RawMessage(`{"status":"Accepted"}`)}}
	handler := serverCommandHandler([]string{"secret"}, ownership, bus, rateLimiterStub{allowed: true}, slog.Default())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/stations/station-1/commands/reset", strings.NewReader(`{"type":"Immediate"}`))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()

	handler(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if bus.published.StationIdentity != "station-1" || bus.published.OwnerID != lease.OwnerID ||
		bus.published.OwnerGeneration != lease.Generation || bus.published.Action != resetAction {
		t.Fatalf("unexpected command: %+v", bus.published)
	}
}

func TestResetCommandAPIRequiresCredentials(t *testing.T) {
	handler := serverCommandHandler([]string{"secret"}, newOwnershipManager(sessionregistry.NewMemory(), "runtime-a", time.Minute, 10*time.Second, slog.Default()), &commandBusStub{}, rateLimiterStub{allowed: true}, slog.Default())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/stations/station-1/commands/reset", strings.NewReader(`{"type":"Immediate"}`))
	response := httptest.NewRecorder()

	handler(response, request)

	if response.Code != http.StatusUnauthorized {
		content, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, body = %s", response.Code, content)
	}
}

func TestResetCommandAPIAcceptsRotatedCredential(t *testing.T) {
	registry := sessionregistry.NewMemory()
	if _, err := registry.Claim(context.Background(), "station-1", "runtime-a", time.Minute); err != nil {
		t.Fatal(err)
	}
	handler := serverCommandHandler(
		[]string{"old-secret", "new-secret"},
		newOwnershipManager(registry, "runtime-a", time.Minute, 10*time.Second, slog.Default()),
		&commandBusStub{result: commandbus.Result{Success: true}},
		rateLimiterStub{allowed: true}, slog.Default(),
	)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/stations/station-1/commands/reset", strings.NewReader(`{"type":"Immediate"}`))
	request.Header.Set("Authorization", "Bearer new-secret")
	response := httptest.NewRecorder()

	handler(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestResetCommandAPIRateLimit(t *testing.T) {
	handler := serverCommandHandler(
		[]string{"secret"},
		newOwnershipManager(sessionregistry.NewMemory(), "runtime-a", time.Minute, 10*time.Second, slog.Default()),
		&commandBusStub{}, rateLimiterStub{allowed: false}, slog.Default(),
	)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/stations/station-1/commands/reset", strings.NewReader(`{"type":"Immediate"}`))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()

	handler(response, request)

	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "60" {
		t.Fatalf("status = %d, retry-after = %q", response.Code, response.Header().Get("Retry-After"))
	}
}
