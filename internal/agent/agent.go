package agent

import "context"

type Agent struct{}

func New() *Agent {
	return &Agent{}
}

func (a *Agent) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
