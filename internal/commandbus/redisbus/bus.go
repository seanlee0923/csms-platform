package redisbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/seanlee0923/csms-platform/internal/commandbus"
)

const consumerGroup = "runtime"
const pendingMinIdle = 5 * time.Second

type Bus struct {
	client    redis.UniversalClient
	prefix    string
	resultTTL time.Duration
}

func New(client redis.UniversalClient, prefix string, resultTTL time.Duration) (*Bus, error) {
	if client == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	if prefix == "" {
		prefix = "csms"
	}
	if resultTTL <= 0 {
		resultTTL = 5 * time.Minute
	}
	return &Bus{client: client, prefix: prefix, resultTTL: resultTTL}, nil
}

func (bus *Bus) Publish(ctx context.Context, command commandbus.Command) error {
	if err := validateCommand(command); err != nil {
		return err
	}
	values := map[string]any{
		"id": command.ID, "station_identity": command.StationIdentity, "owner_id": command.OwnerID,
		"owner_generation": command.OwnerGeneration,
		"action":           command.Action, "payload": string(command.Payload),
		"created_at": command.CreatedAt.UnixNano(), "deadline": timestamp(command.Deadline),
	}
	if err := bus.client.XAdd(ctx, &redis.XAddArgs{
		Stream: bus.commandStream(command.OwnerID), MaxLen: 10000, Approx: true, Values: values,
	}).Err(); err != nil {
		return fmt.Errorf("publish Redis command: %w", err)
	}
	return nil
}

func (bus *Bus) AwaitResult(ctx context.Context, commandID string) (commandbus.Result, error) {
	if commandID == "" {
		return commandbus.Result{}, fmt.Errorf("%w: command ID is required", commandbus.ErrInvalidCommand)
	}
	key := bus.resultKey(commandID)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		value, err := bus.client.RPop(ctx, key).Result()
		if err == nil {
			var result commandbus.Result
			if err := json.Unmarshal([]byte(value), &result); err != nil {
				return commandbus.Result{}, fmt.Errorf("decode Redis command result: %w", err)
			}
			return result, nil
		}
		if !errors.Is(err, redis.Nil) {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return commandbus.Result{}, commandbus.ErrResultTimeout
			}
			return commandbus.Result{}, fmt.Errorf("await Redis command result: %w", err)
		}
		select {
		case <-ctx.Done():
			return commandbus.Result{}, commandbus.ErrResultTimeout
		case <-ticker.C:
		}
	}
}

func (bus *Bus) RunConsumer(ctx context.Context, ownerID string, handler commandbus.Handler) error {
	if ownerID == "" || handler == nil {
		return fmt.Errorf("owner ID and command handler are required")
	}
	stream := bus.commandStream(ownerID)
	if err := bus.client.XGroupCreateMkStream(ctx, stream, consumerGroup, "0").Err(); err != nil && !isBusyGroup(err) {
		return fmt.Errorf("create Redis command consumer group: %w", err)
	}
	lastRecovery := time.Time{}
	for {
		if time.Since(lastRecovery) >= pendingMinIdle {
			messages, _, err := bus.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
				Stream: stream, Group: consumerGroup, Consumer: ownerID,
				MinIdle: pendingMinIdle, Start: "0-0", Count: 10,
			}).Result()
			if errors.Is(err, context.Canceled) {
				return nil
			}
			if err != nil && !errors.Is(err, redis.Nil) {
				return fmt.Errorf("recover pending Redis commands: %w", err)
			}
			if err := bus.processMessages(ctx, stream, messages, handler); err != nil {
				return err
			}
			lastRecovery = time.Now()
		}
		streams, err := bus.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group: consumerGroup, Consumer: ownerID, Streams: []string{stream, ">"},
			Count: 1, Block: time.Second,
		}).Result()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			if errors.Is(err, redis.Nil) {
				continue
			}
			return fmt.Errorf("consume Redis command: %w", err)
		}
		for _, messages := range streams {
			if err := bus.processMessages(ctx, stream, messages.Messages, handler); err != nil {
				return err
			}
		}
	}
}

func (bus *Bus) processMessages(ctx context.Context, stream string, messages []redis.XMessage, handler commandbus.Handler) error {
	for _, message := range messages {
		command, err := decodeCommand(message.Values)
		if err != nil {
			return err
		}
		result, found, err := bus.completed(ctx, command.ID)
		if err != nil {
			return err
		}
		if !found {
			result = runHandler(ctx, handler, command)
		}
		if err := bus.complete(ctx, result); err != nil {
			return err
		}
		if err := bus.client.XAck(ctx, stream, consumerGroup, message.ID).Err(); err != nil {
			return fmt.Errorf("ack Redis command: %w", err)
		}
	}
	return nil
}

func (bus *Bus) complete(ctx context.Context, result commandbus.Result) error {
	if result.CommandID == "" {
		return fmt.Errorf("command result ID is required")
	}
	if result.CompletedAt.IsZero() {
		result.CompletedAt = time.Now().UTC()
	}
	content, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode Redis command result: %w", err)
	}
	key := bus.resultKey(result.CommandID)
	pipeline := bus.client.TxPipeline()
	pipeline.Set(ctx, bus.completionKey(result.CommandID), content, bus.resultTTL)
	pipeline.LPush(ctx, key, content)
	pipeline.Expire(ctx, key, bus.resultTTL)
	if _, err := pipeline.Exec(ctx); err != nil {
		return fmt.Errorf("complete Redis command: %w", err)
	}
	return nil
}

func (bus *Bus) completed(ctx context.Context, commandID string) (commandbus.Result, bool, error) {
	content, err := bus.client.Get(ctx, bus.completionKey(commandID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return commandbus.Result{}, false, nil
	}
	if err != nil {
		return commandbus.Result{}, false, fmt.Errorf("read completed Redis command: %w", err)
	}
	var result commandbus.Result
	if err := json.Unmarshal(content, &result); err != nil {
		return commandbus.Result{}, false, fmt.Errorf("decode completed Redis command: %w", err)
	}
	return result, true, nil
}

func runHandler(parent context.Context, handler commandbus.Handler, command commandbus.Command) commandbus.Result {
	ctx := parent
	cancel := func() {}
	if !command.Deadline.IsZero() {
		ctx, cancel = context.WithDeadline(parent, command.Deadline)
	}
	defer cancel()
	result := handler(ctx, command)
	result.CommandID = command.ID
	if result.CompletedAt.IsZero() {
		result.CompletedAt = time.Now().UTC()
	}
	return result
}

func validateCommand(command commandbus.Command) error {
	if command.ID == "" || command.StationIdentity == "" || command.OwnerID == "" || command.OwnerGeneration == 0 || command.Action == "" || command.CreatedAt.IsZero() {
		return fmt.Errorf("%w: ID, station identity, owner ID, owner generation, action and creation time are required", commandbus.ErrInvalidCommand)
	}
	if !command.Deadline.IsZero() && !command.Deadline.After(command.CreatedAt) {
		return fmt.Errorf("%w: deadline must be after creation time", commandbus.ErrInvalidCommand)
	}
	return nil
}

func decodeCommand(values map[string]any) (commandbus.Command, error) {
	generation, err := decodeGeneration(values["owner_generation"])
	if err != nil {
		return commandbus.Command{}, err
	}
	createdAt, err := unixNano(values["created_at"])
	if err != nil {
		return commandbus.Command{}, err
	}
	deadline, err := unixNano(values["deadline"])
	if err != nil {
		return commandbus.Command{}, err
	}
	command := commandbus.Command{
		ID: stringField(values, "id"), StationIdentity: stringField(values, "station_identity"),
		OwnerID: stringField(values, "owner_id"), OwnerGeneration: uint64(generation),
		Action:  stringField(values, "action"),
		Payload: json.RawMessage(stringField(values, "payload")), CreatedAt: createdAt, Deadline: deadline,
	}
	if err := validateCommand(command); err != nil {
		return commandbus.Command{}, err
	}
	return command, nil
}

// stringField reads a string field from a decoded Redis stream entry,
// returning "" for a missing or nil key. fmt.Sprint(nil) would otherwise
// render a missing key as the literal string "<nil>", which is non-empty
// and so would slip past validateCommand's presence checks.
func stringField(values map[string]any, key string) string {
	raw, ok := values[key]
	if !ok || raw == nil {
		return ""
	}
	return fmt.Sprint(raw)
}

func decodeGeneration(value any) (int64, error) {
	generation, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
	if err != nil || generation < 0 {
		return 0, fmt.Errorf("decode Redis command owner generation")
	}
	return generation, nil
}

func unixNano(value any) (time.Time, error) {
	nanoseconds, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("decode Redis command timestamp: %w", err)
	}
	if nanoseconds == 0 {
		return time.Time{}, nil
	}
	return time.Unix(0, nanoseconds).UTC(), nil
}

func isBusyGroup(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "BUSYGROUP")
}

func timestamp(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}

func (bus *Bus) commandStream(ownerID string) string {
	return bus.prefix + ":commands:" + ownerID
}

func (bus *Bus) resultKey(commandID string) string {
	return bus.prefix + ":command-results:" + commandID
}

func (bus *Bus) completionKey(commandID string) string {
	return bus.prefix + ":command-completions:" + commandID
}
