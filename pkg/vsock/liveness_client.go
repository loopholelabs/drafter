package vsock

const (
	CIDHost  = 2
	CIDGuest = 3
)

func SendLivenessPing(
	vsockCID uint32,
	vsockPort uint32,
) error {
	conn, err := Dial(vsockCID, vsockPort)
	if err != nil {
		return err
	}

	return conn.Close()
}
