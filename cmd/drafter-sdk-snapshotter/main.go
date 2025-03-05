package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/lithammer/shortuuid/v4"
	"github.com/otiai10/copy"
	"github.com/sirupsen/logrus"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	config := firecracker.Config{
		LogLevel:        "Debug",
		KernelImagePath: "/home/pojntfx/Projets/drafter/out/blueprint/vmlinux",
		KernelArgs:      "console=ttyS0 panic=1 pci=off modules=ext4 rootfstype=ext4 root=/dev/vda i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd rootflags=rw printk.devkmsg=on printk_ratelimit=0 printk_ratelimit_burst=0 clocksource=tsc nokaslr lapic=notscdeadline tsc=unstable",
		Drives: firecracker.
			NewDrivesBuilder("/home/pojntfx/Projets/drafter/out/blueprint/rootfs.ext4").
			AddDrive("/home/pojntfx/Projets/drafter/out/blueprint/oci.ext4", false).
			Build(),
		MachineCfg: models.MachineConfiguration{
			VcpuCount:  firecracker.Int64(1),
			MemSizeMib: firecracker.Int64(512),
		},
		NetNS: "/var/run/netns/ark0",
		VsockDevices: []firecracker.VsockDevice{
			{
				Path: "vsock.sock",
				CID:  3,
			},
		},
		JailerCfg: &firecracker.JailerConfig{
			UID:            firecracker.Int(0),
			GID:            firecracker.Int(0),
			ID:             shortuuid.New(),
			NumaNode:       firecracker.Int(0),
			ChrootBaseDir:  "/home/pojntfx/Projets/drafter/out/vms",
			ChrootStrategy: firecracker.NewNaiveChrootStrategy("/home/pojntfx/Projets/drafter/out/blueprint/vmlinux"),
			ExecFile:       "/usr/local/bin/firecracker",
			JailerBinary:   "/usr/local/bin/jailer",
			CgroupVersion:  *firecracker.String("2"),
		},
		NetworkInterfaces: firecracker.NetworkInterfaces{
			{
				StaticConfiguration: &firecracker.StaticNetworkConfiguration{
					MacAddress:  "02:0e:d9:fd:68:3d",
					HostDevName: "tap0",
				},
			},
		},
	}

	if err := os.MkdirAll(filepath.Join(config.JailerCfg.ChrootBaseDir, "firecracker", config.JailerCfg.ID, "root"), os.ModePerm); err != nil {
		panic(err)
	}

	machine, err := firecracker.NewMachine(ctx, config, firecracker.WithLogger(logrus.NewEntry(logrus.New())))
	if err != nil {
		panic(err)
	}

	machine.Handlers.FcInit.Append(firecracker.AddVsocksHandler)

	livenessSocketPath := filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID, "root", "vsock.sock_25")

	livenessLis, err := net.Listen("unix", livenessSocketPath)
	if err != nil {
		panic(err)
	}
	defer livenessLis.Close()

	if err := os.Chown(livenessSocketPath, *config.JailerCfg.UID, *config.JailerCfg.GID); err != nil {
		panic(err)
	}

	agentSocketPath := filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID, "root", "vsock.sock_26")

	agentLis, err := net.Listen("unix", agentSocketPath)
	if err != nil {
		panic(err)
	}
	defer agentLis.Close()

	if err := os.Chown(agentSocketPath, *config.JailerCfg.UID, *config.JailerCfg.GID); err != nil {
		panic(err)
	}

	if err := machine.Start(ctx); err != nil {
		panic(err)
	}
	defer machine.StopVMM()

	go func() {
		if err := machine.Wait(ctx); err != nil && !strings.Contains(err.Error(), "signal: terminated") {
			panic(err)
		}
	}()

	_, err = livenessLis.Accept()
	if err != nil {
		panic(err)
	}

	_, err = agentLis.Accept()
	if err != nil {
		panic(err)
	}

	if err := machine.PauseVM(context.Background()); err != nil {
		panic(err)
	}

	if err := machine.CreateSnapshot(
		ctx,
		"memory.bin",
		"state.bin",
	); err != nil {
		panic(err)
	}

	if err := machine.StopVMM(); err != nil {
		panic(err)
	}

	if err := machine.Wait(ctx); err != nil && !strings.Contains(err.Error(), "signal: terminated") {
		panic(err)
	}

	if err := copy.Copy(
		filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID, "root", "vmlinux"),
		"/home/pojntfx/Projets/drafter/out/package/vmlinux",
	); err != nil {
		panic(err)
	}

	if err := copy.Copy(
		filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID, "root", "rootfs.ext4"),
		"/home/pojntfx/Projets/drafter/out/package/rootfs.ext4",
	); err != nil {
		panic(err)
	}

	if err := copy.Copy(
		filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID, "root", "oci.ext4"),
		"/home/pojntfx/Projets/drafter/out/package/oci.ext4",
	); err != nil {
		panic(err)
	}

	if err := copy.Copy(
		filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID, "root", "memory.bin"),
		"/home/pojntfx/Projets/drafter/out/package/memory.bin",
	); err != nil {
		panic(err)
	}

	if err := copy.Copy(
		filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID, "root", "state.bin"),
		"/home/pojntfx/Projets/drafter/out/package/state.bin",
	); err != nil {
		panic(err)
	}

	if err := os.RemoveAll(filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID)); err != nil {
		panic(err)
	}
}
