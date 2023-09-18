package services

import "context"

type AgentRemote struct {
	BeforeSuspend func(ctx context.Context) error
	AfterResume   func(ctx context.Context) error
}

type Agent struct {
	beforeSuspend func(ctx context.Context) error
	afterResume   func(ctx context.Context) error
}

func NewAgent(
	beforeSuspend func(ctx context.Context) error,
	afterResume func(ctx context.Context) error,
) *Agent {
	return &Agent{
		beforeSuspend: beforeSuspend,
		afterResume:   afterResume,
	}
}

func (a *Agent) BeforeSuspend(ctx context.Context) error {
	return a.beforeSuspend(ctx)
}

func (a *Agent) AfterResume(ctx context.Context) error {
	return a.afterResume(ctx)
}
