package utils

import "github.com/pojntfx/go-nbd/pkg/client"

func GetBlockDevicePadding(resourceSize int64) int64 {
	return (((resourceSize + (client.MaximumBlockSize * 2) - 1) / (client.MaximumBlockSize * 2)) * client.MaximumBlockSize * 2) - resourceSize
}
