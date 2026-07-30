package sessionregistry

import (
	"context"
	"errors"
	"time"
)

var (
	ErrOwnershipConflict = errors.New("station session is owned by another runtime")
	ErrLeaseLost         = errors.New("station session lease was lost")
	ErrNotFound          = errors.New("station session owner was not found")
)

type Lease struct {
	StationIdentity string
	OwnerID         string
	Generation      uint64
	ExpiresAt       time.Time
}

type Registry interface {
	Claim(context.Context, string, string, time.Duration) (Lease, error)
	Renew(context.Context, Lease, time.Duration) (Lease, error)
	Release(context.Context, Lease) error
	Lookup(context.Context, string) (Lease, error)
}
