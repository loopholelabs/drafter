package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/lithammer/shortuuid/v4"
	"github.com/otiai10/copy"
	"github.com/sirupsen/logrus"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	config := firecracker.Config{
		LogLevel: "Debug",
		NetNS:    "/var/run/netns/ark0",
		JailerCfg: &firecracker.JailerConfig{
			UID:            firecracker.Int(0),
			GID:            firecracker.Int(0),
			ID:             shortuuid.New(),
			NumaNode:       firecracker.Int(0),
			ChrootBaseDir:  "out/vms",
			ChrootStrategy: firecracker.NewNaiveChrootStrategy("out/package/vmlinux"),
			ExecFile:       "/usr/local/bin/firecracker",
			JailerBinary:   "/usr/local/bin/jailer",
			CgroupVersion:  *firecracker.String("2"),
		},
		Snapshot: firecracker.SnapshotConfig{
			ResumeVM: true,
		},
		DisableValidation: true,
	}

	if err := os.MkdirAll(filepath.Join(config.JailerCfg.ChrootBaseDir, "firecracker", config.JailerCfg.ID, "root"), os.ModePerm); err != nil {
		panic(err)
	}

	machine, err := firecracker.NewMachine(
		ctx,
		config,
		firecracker.WithLogger(logrus.NewEntry(logrus.New())),
		firecracker.WithSnapshot(
			"memory.bin",
			"state.bin",
		),
	)
	if err != nil {
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

	if err := copy.Copy(
		"out/package/vmlinux",
		filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID, "root", "vmlinux"),
	); err != nil {
		panic(err)
	}

	if err := copy.Copy(
		"out/package/rootfs.ext4",
		filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID, "root", "rootfs.ext4"),
	); err != nil {
		panic(err)
	}

	if err := copy.Copy(
		"out/package/oci.ext4",
		filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID, "root", "oci.ext4"),
	); err != nil {
		panic(err)
	}

	if err := copy.Copy(
		"out/package/memory.bin",
		filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID, "root", "memory.bin"),
	); err != nil {
		panic(err)
	}

	if err := copy.Copy(
		"out/package/state.bin",
		filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID, "root", "state.bin"),
	); err != nil {
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

	if _, err = agentLis.Accept(); err != nil {
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
		"out/package/vmlinux",
	); err != nil {
		panic(err)
	}

	if err := copy.Copy(
		filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID, "root", "rootfs.ext4"),
		"out/package/rootfs.ext4",
	); err != nil {
		panic(err)
	}

	if err := copy.Copy(
		filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID, "root", "oci.ext4"),
		"out/package/oci.ext4",
	); err != nil {
		panic(err)
	}

	if err := copy.Copy(
		filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID, "root", "memory.bin"),
		"out/package/memory.bin",
	); err != nil {
		panic(err)
	}

	if err := copy.Copy(
		filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID, "root", "state.bin"),
		"out/package/state.bin",
	); err != nil {
		panic(err)
	}

	if err := os.RemoveAll(filepath.Join(machine.Cfg.JailerCfg.ChrootBaseDir, "firecracker", machine.Cfg.JailerCfg.ID)); err != nil {
		panic(err)
	}
}
