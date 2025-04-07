package firecracker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/loopholelabs/drafter/pkg/ipc"
	"github.com/loopholelabs/logging/types"
)

var (
	ErrCouldNotStartAgentServer              = errors.New("could not start agent server")
	ErrCouldNotWaitForAcceptingAgent         = errors.New("could not wait for accepting agent")
	ErrCouldNotCloseAcceptingAgent           = errors.New("could not close accepting agent")
	ErrCouldNotOpenLivenessServer            = errors.New("could not open liveness server")
	ErrCouldNotChownLivenessServerVSock      = errors.New("could not change ownership of liveness server VSock")
	ErrCouldNotChownAgentServerVSock         = errors.New("could not change ownership of agent server VSock")
	ErrCouldNotReceiveAndCloseLivenessServer = errors.New("could not receive and close liveness server")
	ErrCouldNotAcceptAgentConnection         = errors.New("could not accept agent connection")
	ErrCouldNotBeforeSuspend                 = errors.New("error before suspend")

	// TODO Dedup
	ErrCouldNotChownVSockPath       = errors.New("could not change ownership of vsock path")
	ErrCouldNotAcceptAgent          = errors.New("could not accept agent")
	ErrCouldNotCallAfterResumeRPC   = errors.New("could not call AfterResume RPC")
	ErrCouldNotCallBeforeSuspendRPC = errors.New("could not call BeforeSuspend RPC")
)

type FirecrackerRPC struct {
	Log               types.Logger
	VMPath            string
	UID               int
	GID               int
	LivenessVSockPort uint32
	AgentVSockPort    uint32

	liveness *ipc.LivenessServer
	agent    *ipc.AgentServer[struct{}, ipc.AgentServerRemote[struct{}], struct{}]
}

func (rpc *FirecrackerRPC) Init() error {
	liveness := ipc.NewLivenessServer(filepath.Join(rpc.VMPath, VSockName), rpc.LivenessVSockPort)

	livenessVSockPath, err := liveness.Open()
	if err != nil {
		return errors.Join(ErrCouldNotOpenLivenessServer, err)
	}
	rpc.liveness = liveness

	if rpc.Log != nil {
		rpc.Log.Debug().Msg("Created liveness server")
	}

	err = os.Chown(livenessVSockPath, rpc.UID, rpc.GID)
	if err != nil {
		return errors.Join(ErrCouldNotChownLivenessServerVSock, err)
	}

	agent, err := ipc.StartAgentServer[struct{}, ipc.AgentServerRemote[struct{}]](
		rpc.Log, filepath.Join(rpc.VMPath, VSockName), rpc.AgentVSockPort, struct{}{},
	)

	if err != nil {
		return errors.Join(ErrCouldNotStartAgentServer, err)
	}
	rpc.agent = agent

	if rpc.Log != nil {
		rpc.Log.Debug().Msg("Created agent server")
	}

	err = os.Chown(agent.VSockPath, rpc.UID, rpc.GID)
	if err != nil {
		return errors.Join(ErrCouldNotChownAgentServerVSock, err)
	}

	return nil
}

func (rpc *FirecrackerRPC) Close() {
	rpc.agent.Close()
	rpc.liveness.Close()
}

func (rpc *FirecrackerRPC) LivenessAndBeforeSuspendAndClose(ctx context.Context, livenessTimeout time.Duration, agentTimeout time.Duration) error {

	receiveCtx, livenessCancel := context.WithTimeout(ctx, livenessTimeout)
	defer livenessCancel()

	err := rpc.liveness.ReceiveAndClose(receiveCtx)
	if err != nil {
		return errors.Join(ErrCouldNotReceiveAndCloseLivenessServer, err)
	}

	if rpc.Log != nil {
		rpc.Log.Debug().Msg("Liveness check OK")
	}

	var acceptingAgent *ipc.AgentConnection[struct{}, ipc.AgentServerRemote[struct{}], struct{}]
	acceptCtx, acceptCancel := context.WithTimeout(ctx, agentTimeout)
	defer acceptCancel()

	acceptingAgent, err = rpc.agent.Accept(acceptCtx, ctx,
		ipc.AgentServerAcceptHooks[ipc.AgentServerRemote[struct{}], struct{}]{},
	)

	if err != nil {
		return errors.Join(ErrCouldNotAcceptAgentConnection, err)
	}
	defer acceptingAgent.Close()

	if rpc.Log != nil {
		rpc.Log.Debug().Msg("RPC Agent accepted")
	}

	acceptingAgentErr := make(chan error, 1)
	go func() {
		err := acceptingAgent.Wait()
		if err != nil {
			// Pass the error back here...
			acceptingAgentErr <- err
		}
	}()

	if rpc.Log != nil {
		rpc.Log.Debug().Msg("Calling Remote BeforeSuspend")
	}

	err = acceptingAgent.Remote.BeforeSuspend(acceptCtx)
	if err != nil {
		return errors.Join(ErrCouldNotBeforeSuspend, err)
	}

	if rpc.Log != nil {
		rpc.Log.Debug().Msg("RPC Closing")
	}

	// Connections need to be closed before creating the snapshot
	rpc.liveness.Close()
	err = acceptingAgent.Close()
	if err != nil {
		return errors.Join(ErrCouldNotCloseAcceptingAgent, err)
	}
	rpc.agent.Close()

	// Check if there was any error
	select {
	case err := <-acceptingAgentErr:
		return err
	default:
	}

	return nil
}
