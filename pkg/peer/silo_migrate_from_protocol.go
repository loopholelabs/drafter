package peer

import (
	"context"
	"fmt"
	"strings"

	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/protocol/packets"
)

func SiloMigrateFrom(pro protocol.Protocol, eventHandler func(e *packets.Event)) (*devicegroup.DeviceGroup, error) {
	fmt.Printf("MigrateFrom...\n")
	tweak := func(index int, name string, schema string) string {
		s := strings.ReplaceAll(schema, "instance-0", "instance-1")
		fmt.Printf("Tweaked schema for %s...\n%s\n\n", name, s)
		return s
	}
	events := func(e *packets.Event) {
		fmt.Printf("Event received %v\n", e)
		eventHandler(e)
	}
	dg, err := devicegroup.NewFromProtocol(context.TODO(), pro, tweak, events, nil, nil)
	fmt.Printf("NewFromProtocol returned %v\n", err)

	dg.WaitForCompletion()

	return dg, err
}
