package testutil

import (
	"context"
	"testing"

	"github.com/loopholelabs/drafter/pkg/network/nat"
	route "github.com/nixigaj/go-default-route"
	"github.com/stretchr/testify/assert"
)

func SetupNAT(t *testing.T, hostInterface string, namespacePrefix string) *nat.Namespaces {

	if hostInterface == "" {
		ifc, err := route.DefaultRouteInterface()
		assert.NoError(t, err)
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
		2, // We only need a couple of namespaces
	)
	assert.NoError(t, err)

	t.Cleanup(func() {
		namespaces.Close()
		// FIXME. For now, there may be "could not release child prefix"
		// assert.NoError(t, err)

		cancel()
	})

	return namespaces
}
