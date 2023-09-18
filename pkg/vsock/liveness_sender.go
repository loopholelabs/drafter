package vsock

import "github.com/mdlayher/vsock"

const (
	CIDHost  = 2
	CIDGuest = 3
)

func SendLivenessPing(
	vsockCID uint32,
	vsockPort uint32,
) error {
	conn, err := vsock.Dial(vsockCID, vsockPort, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	return nil
}
