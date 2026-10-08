package controller

import (
	"context"
	"factorytraffic/internal/domain"
	"factorytraffic/internal/ports"
)

type REST struct{ Store ports.Store }

func (adapter REST) Pending(ctx context.Context, id string) (ports.CommandFeed, error) {
	state, err := adapter.Store.Read(ctx, id)
	if err != nil {
		return ports.CommandFeed{}, err
	}
	feed := ports.CommandFeed{JunctionID: id, Generation: state.Runtime.Generation, Commands: []domain.Command{}}
	if state.Pending != nil && state.Pending.Status == "PENDING" {
		for _, c := range state.Pending.Commands {
			if c.Status == "PENDING" {
				feed.Commands = append(feed.Commands, c)
			}
		}
	}
	return feed, nil
}
