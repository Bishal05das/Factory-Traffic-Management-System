package ports

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"factorytraffic/internal/domain"
)

var ErrNotFound = errors.New("not found")
var ErrExists = errors.New("already exists")

type Result struct {
	Outcome   string `json:"outcome"`
	EventID   string `json:"event_id,omitempty"`
	Revision  int64  `json:"revision"`
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message,omitempty"`
}

type EventRecord struct {
	Hash   string
	Result Result
}

type HistoryItem struct {
	ID         int64            `json:"id"`
	JunctionID string           `json:"junction_id,omitempty"`
	Revision   int64            `json:"junction_revision"`
	Type       string           `json:"event_type"`
	Direction  domain.Direction `json:"direction,omitempty"`
	CommandID  string           `json:"command_id,omitempty"`
	Details    json.RawMessage  `json:"details"`
	At         time.Time        `json:"timestamp"`
}

type Transaction interface {
	Now() time.Time
	ReserveEvent(context.Context, string, string, string, []byte) (*EventRecord, error)
	FinishEvent(context.Context, string, string, Result) error
	Command(context.Context, string) (domain.Command, error)
	Feedback(context.Context, domain.Feedback, string) error
	Save(context.Context, *domain.State, domain.Effects) error
}

type Store interface {
	Create(context.Context, domain.Config) error
	List(context.Context) ([]domain.Config, error)
	Read(context.Context, string) (*domain.State, error)
	Transact(context.Context, string, func(*domain.State, Transaction) error) error
	History(context.Context, string, int64, int) ([]HistoryItem, error)
	Reject(context.Context, string, domain.Audit) error
	Ping(context.Context) error
}
