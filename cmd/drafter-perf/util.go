package main

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"path"
	"time"

	"github.com/loopholelabs/drafter/pkg/network/forwarder"
	"github.com/loopholelabs/drafter/pkg/network/nat"
	rfirecracker "github.com/loopholelabs/drafter/pkg/runtimes/firecracker"
	route "github.com/nixigaj/go-default-route"

	loggingtypes "github.com/loopholelabs/logging/types"
)

/**
 * Pre-requisites
 *  - firecracker works
 *  - blueprints exist
 */
func setupSnapshot(log loggingtypes.Logger, ctx context.Context, netns string, vmConfiguration rfirecracker.VMConfiguration, ioEngineSync bool, blueDir string, snapDir string, waitReady func() error) error {
	firecrackerBin, err := exec.LookPath("firecracker")
	if err != nil {
		return err
	}

	jailerBin, err := exec.LookPath("jailer")
	if err != nil {
		return err
	}

	devices := []rfirecracker.SnapshotDevice{
		{
			Name:   "state",
			Output: path.Join(snapDir, "state"),
		},
		{
			Name:   "memory",
			Output: path.Join(snapDir, "memory"),
		},
		{
			Name:   "kernel",
			Input:  path.Join(blueDir, "vmlinux"),
			Output: path.Join(snapDir, "kernel"),
		},
		{
			Name:   "disk",
			Input:  path.Join(blueDir, "rootfs.ext4"),
			Output: path.Join(snapDir, "disk"),
		},
		{
			Name:   "config",
			Output: path.Join(snapDir, "config"),
		},
		{
			Name:   "oci",
			Input:  path.Join(blueDir, "oci.ext4"),
			Output: path.Join(snapDir, "oci"),
		},
	}

	err = rfirecracker.CreateSnapshot(log, ctx, devices, ioEngineSync,
		vmConfiguration,
		rfirecracker.LivenessConfiguration{
			LivenessVSockPort: uint32(25),
			ResumeTimeout:     time.Minute,
		},
		rfirecracker.FirecrackerMachineConfig{
			FirecrackerBin: firecrackerBin,
			JailerBin:      jailerBin,
			ChrootBaseDir:  snapDir,
			UID:            0,
			GID:            0,
			NetNS:          netns,
			NumaNode:       0,
			CgroupVersion:  2,
			Stdout:         nil,
			Stderr:         nil,
			Stdin:          nil,
		},
		rfirecracker.NetworkConfiguration{
			Interface: "tap0",
			MAC:       "02:0e:d9:fd:68:3d",
		},
		rfirecracker.AgentConfiguration{
			AgentVSockPort: uint32(26),
			ResumeTimeout:  time.Minute,
		},
		waitReady)

	return err
}

func ForwardPort(log loggingtypes.Logger, ns string, protocol string, portFrom int, portTo int) (func(), error) {
	ctx, cancel := context.WithCancel(context.Background())

	hostVeth := "10.0.8.0/22"
	_, hostVethCIDR, err := net.ParseCIDR(hostVeth)
	if err != nil {
		cancel()
		return nil, err
	}

	portForwards := []forwarder.PortForward{
		{
			Netns:        ns,
			InternalPort: fmt.Sprintf("%d", portFrom),
			Protocol:     protocol,
			ExternalAddr: fmt.Sprintf("127.0.0.1:%d", portTo),
		},
	}

	forwardedPorts, err := forwarder.ForwardPorts(
		ctx,
		hostVethCIDR,
		portForwards,

		forwarder.PortForwardHooks{
			OnAfterPortForward: func(portID int, netns, internalIP, internalPort, externalIP, externalPort, protocol string) {
				if log != nil {
					log.Info().
						Int("portID", portID).
						Str("netns", netns).
						Str("internalIP", internalIP).
						Str("internalPort", internalPort).
						Str("externalIP", externalIP).
						Str("externalPort", externalPort).
						Str("protocol", protocol).
						Msg("Forwarding port")
				}
			},
			OnBeforePortUnforward: func(portID int) {
				if log != nil {
					log.Info().
						Int("portID", portID).
						Msg("Unforwarding port")
				}
			},
		},
	)
	if err != nil {
		cancel()
		return nil, err
	}

	return func() {
		err := forwardedPorts.Close()
		if err != nil {
			if log != nil {
				log.Error().Err(err).Msg("could not close forwardedPorts")
			}
		}
		cancel()
	}, nil
}

func SetupNAT(hostInterface string, namespacePrefix string) (*nat.Namespaces, func(), error) {

	if hostInterface == "" {
		ifc, err := route.DefaultRouteInterface()
		if err != nil {
			return nil, nil, err
		}
		hostInterface = ifc.Name
	}

	hostVethCIDR := "10.0.8.0/22"
	namespaceVethCIDR := "10.0.15.0/24"
	blockedSubnetCIDR := "10.0.15.0/24"
	namespaceInterface := "tap0"
	namespaceInterfaceGateway := "172.16.0.1"
	namespaceInterfaceNetmask := 30
	namespaceInterfaceIP := "172.16.0.2"
	namespaceInterfaceMAC := "02:0e:d9:fd:68:3d"
	allowIncomingTraffic := true

	ctx, cancel := context.WithCancel(context.Background())

	namespaces, err := nat.CreateNAT(
		ctx,
		context.Background(), // Never give up on rescue operations

		nat.TranslationConfiguration{
			HostInterface:             hostInterface,
			HostVethCIDR:              hostVethCIDR,
			NamespaceVethCIDR:         namespaceVethCIDR,
			BlockedSubnetCIDR:         blockedSubnetCIDR,
			NamespaceInterface:        namespaceInterface,
			NamespaceInterfaceGateway: namespaceInterfaceGateway,
			NamespaceInterfaceNetmask: uint32(namespaceInterfaceNetmask),
			NamespaceInterfaceIP:      namespaceInterfaceIP,
			NamespaceInterfaceMAC:     namespaceInterfaceMAC,
			NamespacePrefix:           namespacePrefix,
			AllowIncomingTraffic:      allowIncomingTraffic,
		},

		nat.CreateNamespacesHooks{
			OnBeforeCreateNamespace: func(id string) {
				//				log.Println("Creating namespace", id)
			},
			OnBeforeRemoveNamespace: func(id string) {
				//				log.Println("Removing namespace", id)
			},
		},
		32, // We need a few namespaces here...
	)
	if err != nil {
		cancel()
		return nil, nil, err
	}

	return namespaces, func() {
		err := namespaces.Close()
		if err != nil {
			fmt.Printf("Could not close namespace %v", err)
			// FIXME. For now, there may be "could not release child prefix"
			// assert.NoError(t, err)
		}
		cancel()
	}, nil
}
