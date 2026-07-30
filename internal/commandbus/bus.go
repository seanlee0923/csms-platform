package commandbus

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalidCommand = errors.New("invalid command")
	ErrResultTimeout  = errors.New("command result timed out")
)

type Command struct {
	ID              string
	StationIdentity string
	OwnerID         string
	OwnerGeneration uint64
	Action          string
	Payload         json.RawMessage
	CreatedAt       time.Time
	Deadline        time.Time
}

type Result struct {
	CommandID        string
	Success          bool
	Payload          json.RawMessage
	ErrorCode        string
	ErrorDescription string
	CompletedAt      time.Time
}

type Handler func(context.Context, Command) Result

type Bus interface {
	Publish(context.Context, Command) error
	AwaitResult(context.Context, string) (Result, error)
	RunConsumer(context.Context, string, Handler) error
}
