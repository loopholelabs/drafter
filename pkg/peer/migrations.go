package peer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/loopholelabs/drafter/pkg/mounter"
	"github.com/loopholelabs/drafter/pkg/registry"
	"github.com/loopholelabs/drafter/pkg/snapshotter"
	"github.com/loopholelabs/logging"
	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/protocol/packets"
	"golang.org/x/sys/unix"
)

type MigrateFromDevice struct {
	Name      string `json:"name"`
	Base      string `json:"base"`
	Overlay   string `json:"overlay"`
	State     string `json:"state"`
	BlockSize uint32 `json:"blockSize"`
	Shared    bool   `json:"shared"`
}

// expose a Silo Device as a file within the vm directory
func exposeSiloDeviceAsFile(vmpath string, name string, devicePath string) error {
	deviceInfo, err := os.Stat(devicePath)
	if err != nil {
		return errors.Join(snapshotter.ErrCouldNotGetDeviceStat, err)
	}

	deviceStat, ok := deviceInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return ErrCouldNotGetNBDDeviceStat
	}

	err = unix.Mknod(filepath.Join(vmpath, name), unix.S_IFBLK|0666, int(deviceStat.Rdev))
	if err != nil {
		return errors.Join(ErrCouldNotCreateDeviceNode, err)
	}

	return nil
}

/**
 * This creates a Silo Dev Schema given a MigrateFromDevice
 * If you want to change the type of storage used, or Silo options, you can do so here.
 *
 */
func createSiloDevSchema(i *MigrateFromDevice) (*config.DeviceSchema, error) {
	stat, err := os.Stat(i.Base)
	if err != nil {
		return nil, errors.Join(mounter.ErrCouldNotGetBaseDeviceStat, err)
	}

	ds := &config.DeviceSchema{
		Name:      i.Name,
		BlockSize: fmt.Sprintf("%v", i.BlockSize),
		Expose:    true,
		Size:      fmt.Sprintf("%v", stat.Size()),
	}
	if strings.TrimSpace(i.Overlay) == "" || strings.TrimSpace(i.State) == "" {
		ds.System = "file"
		ds.Location = i.Base
	} else {
		err := os.MkdirAll(filepath.Dir(i.Overlay), os.ModePerm)
		if err != nil {
			return nil, errors.Join(mounter.ErrCouldNotCreateOverlayDirectory, err)
		}

		err = os.MkdirAll(filepath.Dir(i.State), os.ModePerm)
		if err != nil {
			return nil, errors.Join(mounter.ErrCouldNotCreateStateDirectory, err)
		}

		ds.System = "sparsefile"
		ds.Location = i.Overlay

		ds.ROSource = &config.DeviceSchema{
			Name:     i.State,
			System:   "file",
			Location: i.Base,
			Size:     fmt.Sprintf("%v", stat.Size()),
		}
	}
	return ds, nil
}

/**
 * 'migrate' from the local filesystem.
 *
 */
func migrateFromFS(vmpath string, devices []MigrateFromDevice) (*devicegroup.DeviceGroup, error) {
	siloDeviceSchemas := make([]*config.DeviceSchema, 0)
	for _, input := range devices {
		if input.Shared {
			// Deal with shared devices here...
			err := exposeSiloDeviceAsFile(vmpath, input.Name, input.Base)
			if err != nil {
				return nil, err
			}
		} else {
			ds, err := createSiloDevSchema(&input)
			if err != nil {
				return nil, err
			}
			siloDeviceSchemas = append(siloDeviceSchemas, ds)
		}
	}

	// Create a silo deviceGroup from all the schemas
	dg, err := devicegroup.NewFromSchema(siloDeviceSchemas, nil, nil)
	if err != nil {
		return nil, err
	}

	// For state, add some logging...
	log := logging.New(logging.Zerolog, "drafter", os.Stderr)
	log.SetLevel(types.TraceLevel)
	di := dg.GetDeviceInformationByName("state")
	logger := modules.NewLogger(di.Volatility, "state", log)
	di.Exp.SetProvider(logger)

	for _, input := range siloDeviceSchemas {
		dev := dg.GetExposedDeviceByName(input.Name)
		err = exposeSiloDeviceAsFile(vmpath, input.Name, filepath.Join("/dev", dev.Device()))
		if err != nil {
			return nil, err
		}
	}
	return dg, nil
}

/**
 * Migrate FROM a pipe
 * NB: You should call dg.WaitForCompletion() later to ensure migrations are finished
 */
func migrateFromPipe(vmpath string, ctx context.Context, readers []io.Reader, writers []io.Writer) (*devicegroup.DeviceGroup, error) {
	ready := make(chan bool)
	pro := protocol.NewRW(ctx, readers, writers, nil)

	// Start a goroutine to do the protocol Handle()
	go func() {
		err := pro.Handle()
		if err != nil && !errors.Is(err, io.EOF) {
			// This deserves a warning log message, but will result in other errors being returned
			// so can be ignored here.
		}
	}()

	// TODO: This schema tweak function should be exposed / passed in
	tweak := func(index int, name string, schema string) string {
		s := strings.ReplaceAll(schema, "instance-0", "instance-1")
		fmt.Printf("Tweaked schema for %s...\n%s\n\n", name, s)
		return string(s)
	}

	events := func(e *packets.Event) {}
	cdh := func(data []byte) {
		if len(data) == 1 && data[0] == byte(registry.EventCustomTransferAuthority) {
			close(ready)
		}
	}
	dg, err := devicegroup.NewFromProtocol(ctx, pro, tweak, events, cdh, nil, nil)
	if err != nil {
		return nil, err
	}

	// Expose all devices
	for _, n := range dg.GetAllNames() {
		dev := dg.GetExposedDeviceByName(n)
		if dev != nil {
			err := exposeSiloDeviceAsFile(vmpath, n, filepath.Join("/dev", dev.Device()))
			if err != nil {
				return nil, err
			}
		}
	}

	// Wait either until EventCustomTransferAuthority or context cancelled.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-ready:
	}

	return dg, nil
}

/**
 * Migrate TO a pipe
 *
 */
func migrateToPipe(ctx context.Context, readers []io.Reader, writers []io.Writer,
	dg *devicegroup.DeviceGroup, concurrency int, onProgress func(p map[string]*migrator.MigrationProgress),
	vmState *VMStateMgr, devices []mounter.MigrateToDevice) error {

	// Create a protocol for use by Silo
	pro := protocol.NewRW(ctx, readers, writers, nil)

	// Start a goroutine to do the protocol Handle()
	go func() {
		err := pro.Handle()
		if err != nil && !errors.Is(err, io.EOF) {
			// Deserves a log, but not critical, as it'll get returned in other errors
		}
	}()

	// Start a migration to the protocol. This will send all schema info etc
	err := dg.StartMigrationTo(pro)
	if err != nil {
		return err
	}

	// Do the main migration of the data...
	err = dg.MigrateAll(concurrency, onProgress)
	if err != nil {
		return err
	}

	dirtyDevices := make(map[string]*DeviceStatus, 0)
	for _, d := range devices {
		dirtyDevices[d.Name] = &DeviceStatus{
			CycleThrottle:  d.CycleThrottle,
			MinCycles:      d.MinCycles,
			MaxCycles:      d.MaxCycles,
			MaxDirtyBlocks: d.MaxDirtyBlocks,
		}
	}

	// When we are ready to transfer authority, we send a single Custom Event here.
	authTransfer := func() error {
		return dg.SendCustomData([]byte{byte(registry.EventCustomTransferAuthority)})
	}

	dm := NewDirtyManager(vmState, dirtyDevices, authTransfer)

	err = dg.MigrateDirty(&devicegroup.MigrateDirtyHooks{
		PreGetDirty:      dm.PreGetDirty,
		PostGetDirty:     dm.PostGetDirty,
		PostMigrateDirty: dm.PostMigrateDirty,
		Completed:        func(name string) {},
	})
	if err != nil {
		return err
	}

	// Send Silo completion events for the devices. This will trigger any S3 sync behaviour etc.
	err = dg.Completed()
	if err != nil {
		return err
	}

	return nil
}
