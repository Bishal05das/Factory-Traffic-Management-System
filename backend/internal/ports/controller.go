package ports

import (
	"context"
	"factorytraffic/internal/domain"
)

type CommandFeed struct {
	JunctionID string           `json:"junction_id"`
	Generation int64            `json:"generation"`
	Commands   []domain.Command `json:"commands"`
}

// Controller separates the durable command transport from traffic decisions.
// A future MQTT adapter can publish this feed without rewriting the engine.
type Controller interface {
	Pending(context.Context, string) (CommandFeed, error)
}
