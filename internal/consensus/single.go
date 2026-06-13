package consensus

import "context"

type SingleNodeEngine struct {
	address string
}

func NewSingleNodeEngine(address string) *SingleNodeEngine {
	return &SingleNodeEngine{address: address}
}

func (e *SingleNodeEngine) Apply(ctx context.Context, command Command) (Result, error) {
	return Result{}, nil
}

func (e *SingleNodeEngine) Query(ctx context.Context, query Query) (Result, error) {
	return Result{}, nil
}

func (e *SingleNodeEngine) IsLeader() bool {
	return true
}

func (e *SingleNodeEngine) LeaderAddress() string {
	return e.address
}
