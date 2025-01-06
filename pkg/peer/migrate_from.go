package peer

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/loopholelabs/drafter/pkg/mounter"
)

func (peer *Peer[L, R, G]) MigrateFrom(
	ctx context.Context,
	devices []MigrateFromDevice,
	readers []io.Reader,
	writers []io.Writer,
	hooks mounter.MigrateFromHooks,
) (
	migratedPeer *MigratedPeer[L, R, G],
	errs error,
) {

	migratedPeer = &MigratedPeer[L, R, G]{
		Wait: func() error {
			return nil
		},

		devices: devices,
		runner:  peer.runner,
	}

	migratedPeer.Close = func() (errs error) {
		// We have to close the runner before we close the devices
		if err := peer.runner.Close(); err != nil {
			return err
		}

		// Close any Silo devices
		migratedPeer.DgLock.Lock()
		if migratedPeer.Dg != nil {
			err := migratedPeer.Dg.CloseAll()
			if err != nil {
				migratedPeer.DgLock.Unlock()
				return err
			}
		}
		migratedPeer.DgLock.Unlock()
		return nil
	}

	// Migrate the devices from a protocol
	if len(readers) > 0 && len(writers) > 0 {
		protocolCtx, cancelProtocolCtx := context.WithCancel(ctx)

		// TODO: This schema tweak function should be exposed / passed in
		tweak := func(index int, name string, schema string) string {
			s := strings.ReplaceAll(schema, "instance-0", "instance-1")
			fmt.Printf("Tweaked schema for %s...\n%s\n\n", name, s)
			return string(s)
		}

		dg, err := migrateFromPipe(nil, nil, migratedPeer.runner.VMPath, protocolCtx, readers, writers, tweak)
		if err != nil {
			return nil, err
		}

		migratedPeer.Wait = sync.OnceValue(func() error {
			defer cancelProtocolCtx()

			fmt.Printf("Waiting for dg completion...\n")
			err := dg.WaitForCompletion()
			if err != nil {
				return err
			}

			fmt.Printf("Migrations completed.\n")

			// Save dg for future migrations.
			migratedPeer.DgLock.Lock()
			migratedPeer.Dg = dg
			migratedPeer.DgLock.Unlock()
			return nil
		})
		/*
			names := dg.GetAllNames()
			for _, n := range names {
				di := dg.GetDeviceInformationByName(n)

				a1, a2 := di.WaitingCacheLocal.Availability()
				fmt.Printf("[%s] Waiting cache %d %d / %d\n", n, a1, a2, di.NumBlocks)

				// Read straight from device...
				fp, err := os.Open(fmt.Sprintf("/dev/%s", di.Exp.Device()))
				if err != nil {
					panic(err)
				}

				size := di.Prov.Size()
				buffer := make([]byte, size)
				fp.ReadAt(buffer, 0)
				hash := sha256.Sum256(buffer)
				log.Printf("DATA[%s] %d hash %x\n", n, size, hash)

				fp.Close()
			}
		*/
	}

	//
	// IF all devices are local
	//

	if len(readers) == 0 && len(writers) == 0 {
		dg, err := migrateFromFS(nil, nil, migratedPeer.runner.VMPath, devices)
		if err != nil {
			return nil, err
		}

		// Save dg for later usage, when we want to migrate from here etc
		migratedPeer.DgLock.Lock()
		migratedPeer.Dg = dg
		migratedPeer.DgLock.Unlock()

		if hook := hooks.OnLocalAllDevicesRequested; hook != nil {
			hook()
		}
	}

	fmt.Printf("Ready to use VM\n")

	return
}
