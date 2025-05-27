package main

import (
	"fmt"

	"github.com/loopholelabs/drafter/pkg/testutil"
	"github.com/muesli/gotable"
)

func showDeviceStats(dummyMetrics *testutil.DummyMetrics, name string) {
	devTab := gotable.NewTable([]string{"Name",
		"In R Ops", "In R MB", "In W Ops", "In W MB",
		"DskR Ops", "DskR MB", "DskW Ops", "DskW MB",
		"Chg  Blk", "Chg  MB",
	},
		[]int64{-20, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8},
		"No data in table.")

	// Show the important devices
	for _, r := range []string{"disk", "oci", "memory"} {
		dm := getSiloDeviceStats(dummyMetrics, name, r)
		if dm != nil {
			devTab.AppendRow([]interface{}{
				r,
				fmt.Sprintf("%d", dm.InReadOps),
				fmt.Sprintf("%.1f", float64(dm.InReadBytes)/(1024*1024)),
				fmt.Sprintf("%d", dm.InWriteOps),
				fmt.Sprintf("%.1f", float64(dm.InWriteBytes)/(1024*1024)),

				fmt.Sprintf("%d", dm.DiskReadOps),
				fmt.Sprintf("%.1f", float64(dm.DiskReadBytes)/(1024*1024)),
				fmt.Sprintf("%d", dm.DiskWriteOps),
				fmt.Sprintf("%.1f", float64(dm.DiskWriteBytes)/(1024*1024)),

				fmt.Sprintf("%d", dm.ChangedBlocks),
				fmt.Sprintf("%.1f", float64(dm.ChangedBytes)/(1024*1024)),
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

/**
 * Grab out some silo stats from the metrics system
 *
 */
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
