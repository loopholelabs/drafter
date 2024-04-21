package utils

const (
	// See https://pkg.go.dev/github.com/pojntfx/go-nbd@v0.3.2/pkg/client#pkg-constants
	// We still need to set paddings even if Silo supports adding these offsets automatically because the runner doesn't use Silo
	minimumBlockSize = 512  // This is the minimum value that works in practice, else the client stops with "invalid argument"
	maximumBlockSize = 4096 // This is the maximum value that works in practice, else the client stops with "invalid argument"
)

func GetBlockDevicePadding(resourceSize int64) int64 {
	return (((resourceSize + (maximumBlockSize * 2) - 1) / (maximumBlockSize * 2)) * maximumBlockSize * 2) - resourceSize
}
