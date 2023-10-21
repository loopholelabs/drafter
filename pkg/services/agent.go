package services

import (
	"context"
	"log"
)

type AgentRemote struct {
	BeforeSuspend func(ctx context.Context) error
	AfterResume   func(ctx context.Context) error
}

type Agent struct {
	beforeSuspend func(ctx context.Context) error
	afterResume   func(ctx context.Context) error

	verbose bool
}

func NewAgent(
	beforeSuspend func(ctx context.Context) error,
	afterResume func(ctx context.Context) error,

	verbose bool,
) *Agent {
	return &Agent{
		beforeSuspend: beforeSuspend,
		afterResume:   afterResume,

		verbose: verbose,
	}
}

func (a *Agent) BeforeSuspend(ctx context.Context) error {
	if a.verbose {
		log.Println("BeforeSuspend()")
	}

	return a.beforeSuspend(ctx)
}

func (a *Agent) AfterResume(ctx context.Context) error {
	if a.verbose {
		log.Println("AfterResume()")
	}

	return a.afterResume(ctx)
}
