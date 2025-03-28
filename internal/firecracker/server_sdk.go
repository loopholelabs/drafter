package firecracker

import (
	"context"

	sdk "github.com/firecracker-microvm/firecracker-go-sdk"
)

type FirecrackerServerSDK struct {
	VMPath string
	VMPid  int

	Wait  func() error
	Close func() error
}

func StartFirecrackerServerSDK(
	ctx context.Context,
	firecrackerBin string,
	jailerBin string,
	chrootBaseDir string,
	uid int,
	gid int,
	netns string,
	numaNode int,
	cgroupVersion int,
	enableOutput bool,
	enableInput bool,
) (server *FirecrackerServerSDK, errs error) {

	// TODO
	sdk.Bool(true)

	return nil, nil
}
