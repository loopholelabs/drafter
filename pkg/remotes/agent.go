package remotes

import "context"

type AgentRemote struct {
	BeforeSuspend func(ctx context.Context) error
	AfterResume   func(ctx context.Context) error
}
