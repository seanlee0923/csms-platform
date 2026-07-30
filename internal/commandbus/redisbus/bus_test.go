package redisbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/seanlee0923/csms-platform/internal/commandbus"
)

func TestRedisBusRoutesCommandAndCorrelatesResult(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { client.Close() })
	bus, err := New(client, "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	consumerCtx, cancelConsumer := context.WithCancel(context.Background())
	t.Cleanup(cancelConsumer)
	handled := make(chan commandbus.Command, 1)
	go func() {
		_ = bus.RunConsumer(consumerCtx, "runtime-a", func(_ context.Context, command commandbus.Command) commandbus.Result {
			handled <- command
			return commandbus.Result{Success: true, Payload: json.RawMessage(`{"status":"Accepted"}`)}
		})
	}()
	command := commandbus.Command{
		ID: "command-1", StationIdentity: "station-1", OwnerID: "runtime-a", Action: "Reset",
		OwnerGeneration: 7,
		Payload:         json.RawMessage(`{"type":"Immediate"}`), CreatedAt: time.Now().UTC(),
		Deadline: time.Now().UTC().Add(time.Second),
	}
	if err := bus.Publish(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := bus.AwaitResult(ctx, command.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.CommandID != command.ID || string(result.Payload) != `{"status":"Accepted"}` {
		t.Fatalf("unexpected result: %+v", result)
	}
	select {
	case got := <-handled:
		if got.ID != command.ID || got.Action != command.Action || got.OwnerGeneration != command.OwnerGeneration {
			t.Fatalf("unexpected command: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("handler did not receive command")
	}
	cancelConsumer()
}

func TestRedisBusRecoversPendingCommand(t *testing.T) {
	redisURL := os.Getenv("CSMS_REDIS_INTEGRATION_URL")
	if redisURL == "" {
		t.Skip("CSMS_REDIS_INTEGRATION_URL is not set")
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { client.Close() })
	prefix := fmt.Sprintf("csms-recovery-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		var cursor uint64
		for {
			keys, next, scanErr := client.Scan(context.Background(), cursor, prefix+":*", 100).Result()
			if scanErr != nil {
				return
			}
			if len(keys) > 0 {
				client.Del(context.Background(), keys...)
			}
			cursor = next
			if cursor == 0 {
				return
			}
		}
	})
	bus, err := New(client, prefix, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	command := commandbus.Command{
		ID: "pending-command", StationIdentity: "station-1", OwnerID: "runtime-a",
		OwnerGeneration: 1, Action: "Reset", Payload: json.RawMessage(`{"type":"Immediate"}`),
		CreatedAt: now, Deadline: now.Add(20 * time.Second),
	}
	if err := bus.Publish(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	stream := bus.commandStream(command.OwnerID)
	if err := client.XGroupCreate(context.Background(), stream, consumerGroup, "0").Err(); err != nil {
		t.Fatal(err)
	}
	messages, err := client.XReadGroup(context.Background(), &redis.XReadGroupArgs{
		Group: consumerGroup, Consumer: "stopped-consumer",
		Streams: []string{stream, ">"}, Count: 1,
	}).Result()
	if err != nil || len(messages) != 1 || len(messages[0].Messages) != 1 {
		t.Fatalf("create pending message: messages=%v err=%v", messages, err)
	}

	consumerCtx, cancelConsumer := context.WithCancel(context.Background())
	defer cancelConsumer()
	handled := make(chan struct{}, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- bus.RunConsumer(consumerCtx, command.OwnerID, func(context.Context, commandbus.Command) commandbus.Result {
			handled <- struct{}{}
			return commandbus.Result{Success: true}
		})
	}()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancelWait()
	result, err := bus.AwaitResult(waitCtx, command.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("unexpected recovered result: %+v", result)
	}
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("recovered command was not handled")
	}
	cancelConsumer()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("consumer did not stop")
	}
}

func TestRedisBusAwaitResultHonorsContextTimeout(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { client.Close() })
	bus, err := New(client, "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := bus.AwaitResult(ctx, "missing"); !errors.Is(err, commandbus.ErrResultTimeout) {
		t.Fatalf("got %v", err)
	}
}

func TestDecodeCommandRejectsEntryMissingRequiredField(t *testing.T) {
	// "id" is intentionally absent, simulating a corrupted/malformed stream
	// entry. Before the fix, a missing map key rendered as the literal
	// string "<nil>" via fmt.Sprint, which is non-empty and so slipped
	// past validateCommand's presence check.
	values := map[string]any{
		"station_identity": "station-1", "owner_id": "runtime-a", "owner_generation": "1",
		"action": "Reset", "payload": "{}",
		"created_at": fmt.Sprint(time.Now().UTC().UnixNano()), "deadline": "0",
	}
	if _, err := decodeCommand(values); !errors.Is(err, commandbus.ErrInvalidCommand) {
		t.Fatalf("got %v, want %v for a stream entry missing the id field", err, commandbus.ErrInvalidCommand)
	}
}

func TestRedisBusRejectsInvalidCommand(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { client.Close() })
	bus, err := New(client, "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(context.Background(), commandbus.Command{}); !errors.Is(err, commandbus.ErrInvalidCommand) {
		t.Fatalf("got %v", err)
	}
}
