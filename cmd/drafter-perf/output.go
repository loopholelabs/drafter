package main

import (
	"fmt"

	"github.com/loopholelabs/drafter/pkg/testutil"
	"github.com/muesli/gotable"
)

func showDeviceStats(dummyMetrics *testutil.DummyMetrics, name string) {
	devTab := gotable.NewTable([]string{"Name",
		"In R Ops", "In R", "In W Ops", "In W",
		"DskR Ops", "DskR", "DskW Ops", "DskW",
		"Chg Blk", "Chg", "S3PutOps", "S3Put", "S3GetOps", "S3Get",
	},
		[]int64{-16, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8},
		"No data in table.")

	// Show the important devices
	for _, r := range []string{"disk", "oci", "memory"} {
		dm := getSiloDeviceStats(dummyMetrics, name, r)
		if dm != nil {
			s3puts := 0
			s3putBytes := uint64(0)
			s3gets := 0
			s3getBytes := uint64(0)
			s3s := dummyMetrics.GetS3Storage(name, fmt.Sprintf("s3sync_%s", r))
			if s3s != nil {
				smet := s3s.Metrics()
				s3puts = int(smet.BlocksWCount)
				s3putBytes = smet.BlocksWBytes
			}
			s3g := dummyMetrics.GetS3Storage(name, fmt.Sprintf("s3grab_%s", r))
			if s3g != nil {
				smet := s3g.Metrics()
				s3gets = int(smet.BlocksRCount)
				s3getBytes = smet.BlocksRBytes
			}

			devTab.AppendRow([]interface{}{
				r,
				fmt.Sprintf("%d", dm.InReadOps),
				formatBytes(dm.InReadBytes),
				fmt.Sprintf("%d", dm.InWriteOps),
				formatBytes(dm.InWriteBytes),

				fmt.Sprintf("%d", dm.DiskReadOps),
				formatBytes(dm.DiskReadBytes),
				fmt.Sprintf("%d", dm.DiskWriteOps),
				formatBytes(dm.DiskWriteBytes),

				fmt.Sprintf("%d", dm.ChangedBlocks),
				formatBytes(dm.ChangedBytes),

				fmt.Sprintf("%d", s3puts),
				formatBytes(s3putBytes),

				fmt.Sprintf("%d", s3gets),
				formatBytes(s3getBytes),
			})
		}
	}

	devTab.Print()

	fmt.Printf("\n")
}

type DeviceMetrics struct {
	DiskReadOps    uint64
	DiskReadBytes  uint64
	DiskWriteOps   uint64
	DiskWriteBytes uint64
	InReadOps      uint64
	InReadBytes    uint64
	InWriteOps     uint64
	InWriteBytes   uint64
	ChangedBlocks  uint64
	ChangedBytes   uint64
}

// getSiloDeviceStats
func getSiloDeviceStats(dummyMetrics *testutil.DummyMetrics, name string, deviceName string) *DeviceMetrics {
	metrics := dummyMetrics.GetMetrics(name, deviceName).GetMetrics()
	devMetrics := dummyMetrics.GetMetrics(name, fmt.Sprintf("device_%s", deviceName)).GetMetrics()
	rodev := dummyMetrics.GetMetrics(name, fmt.Sprintf("device_rodev_%s", deviceName))

	// Now grab out what we need from these...
	dm := &DeviceMetrics{
		InReadOps:      metrics.ReadOps,
		InReadBytes:    metrics.ReadBytes,
		InWriteOps:     metrics.WriteOps,
		InWriteBytes:   metrics.WriteBytes,
		DiskReadOps:    devMetrics.ReadOps,
		DiskReadBytes:  devMetrics.ReadBytes,
		DiskWriteOps:   devMetrics.WriteOps,
		DiskWriteBytes: devMetrics.WriteBytes,
	}

	// If we are using COW, then add these on to the totals.
	if rodev != nil {
		devROMetrics := rodev.GetMetrics()

		dm.DiskReadOps += devROMetrics.ReadOps
		dm.DiskReadBytes += devROMetrics.ReadBytes
		dm.DiskWriteOps += devROMetrics.WriteOps
		dm.DiskWriteBytes += devROMetrics.WriteBytes

		cow := dummyMetrics.GetCow(name, deviceName)

		if cow != nil {
			changedBlocks, changedBytes, err := cow.GetDifference()
			if err == nil {
				dm.ChangedBlocks = uint64(changedBlocks)
				dm.ChangedBytes = uint64(changedBytes)
			}
		}

	}
	return dm
}

// formatBytes nicely for display
func formatBytes(b uint64) string {
	if b < 1024 {
		return fmt.Sprintf("%db", b)
	} else if b < 1024*1024 {
		return fmt.Sprintf("%.1fk", float64(b)/1024)
	} else if b < 1024*1024*1024 {
		return fmt.Sprintf("%.1fm", float64(b)/(1024*1024))
	} else {
		return fmt.Sprintf("%.1fg", float64(b)/(1024*1024*1024))
	}
}
