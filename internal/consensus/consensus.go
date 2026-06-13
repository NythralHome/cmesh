package consensus

import "context"

type Command struct {
	ID      string
	Type    string
	Payload []byte
}

type Query struct {
	Type    string
	Payload []byte
}

type Result struct {
	Payload []byte
}

type Engine interface {
	Apply(ctx context.Context, command Command) (Result, error)
	Query(ctx context.Context, query Query) (Result, error)
	IsLeader() bool
	LeaderAddress() string
}
