package liveness

import "github.com/mdlayher/vsock"

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
