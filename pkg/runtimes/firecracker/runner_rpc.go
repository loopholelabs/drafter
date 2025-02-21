package firecracker

import (
	"errors"

	"github.com/loopholelabs/drafter/pkg/ipc"
)

type RunnerRPC[L ipc.AgentServerLocal, R ipc.AgentServerRemote[G], G any] struct {
	agent          *ipc.AgentServer[L, R, G]
	acceptingAgent *ipc.AcceptingAgentServer[L, R, G]
	Remote         *R
}

func (rrpc *RunnerRPC[L, R, G]) Close() error {
	if rrpc.acceptingAgent != nil {
		err := rrpc.acceptingAgent.Close()
		if err != nil {
			return errors.Join(ErrCouldNotCloseAcceptingAgent, err)
		}
	}

	if rrpc.agent != nil {
		rrpc.agent.Close()
	}
	return nil
}

func (rrpc *RunnerRPC[L, R, G]) Wait() error {
	if rrpc.acceptingAgent != nil {
		return rrpc.acceptingAgent.Wait()
	}
	return nil
}
