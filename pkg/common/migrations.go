package common

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/loopholelabs/logging/types"
	"github.com/loopholelabs/silo/pkg/storage/config"
	"github.com/loopholelabs/silo/pkg/storage/devicegroup"
	"github.com/loopholelabs/silo/pkg/storage/metrics"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/protocol/packets"
	"golang.org/x/sys/unix"
)

var (
	ErrCouldNotCreateDeviceDirectory  = errors.New("could not create device directory")
	ErrCouldNotGetBaseDeviceStat      = errors.New("could not get base device statistics")
	ErrCouldNotCreateOverlayDirectory = errors.New("could not create overlay directory")
	ErrCouldNotCreateStateDirectory   = errors.New("could not create state directory")
)

type MigrateToDevice struct {
	Name string `json:"name"`

	MaxDirtyBlocks int `json:"maxDirtyBlocks"`
	MinCycles      int `json:"minCycles"`
	MaxCycles      int `json:"maxCycles"`

	CycleThrottle time.Duration `json:"cycleThrottle"`
}

type MigrateFromDevice struct {
	Name      string `json:"name"`
	Base      string `json:"base"`
	Overlay   string `json:"overlay"`
	State     string `json:"state"`
	BlockSize uint32 `json:"blockSize"`
	Shared    bool   `json:"shared"`

	UseSparseFile       bool   `json:"usesparsefile"`
	AnyOrder            bool   `json:"anyorder"`
	UseWriteCache       bool   `json:"usewritecache"`
	WriteCacheMin       string `json:"writecachemin"`
	WriteCacheMax       string `json:"writecachemax"`
	WriteCacheBlocksize string `json:"writecacheblocksize"`

	SharedBase bool `json:"sharedbase"`

	SkipSilo bool `json:"skipsilo"`

	// General S3 setup
	S3Sync        bool   `json:"s3sync"`
	S3AccessKey   string `json:"s3accesskey"`
	S3SecretKey   string `json:"s3secretkey"`
	S3Endpoint    string `json:"s3endpoint"`
	S3Secure      bool   `json:"s3secure"`
	S3Bucket      string `json:"s3bucket"`
	S3Concurrency int    `json:"s3concurrency"`
	S3Prefix      string `json:"s3prefix"`

	// S3 sync config
	S3OnlyDirty   bool   `json:"s3onlydirty"`
	S3BlockShift  int    `json:"s3blockshift"`
	S3MaxAge      string `json:"s3maxage"`
	S3MinChanged  int    `json:"s3minchanged"`
	S3Limit       int    `json:"s3limit"`
	S3CheckPeriod string `json:"s3checkperiod"`
}

var (
	ErrCouldNotGetNBDDeviceStat = errors.New("could not get NBD device stat")
	ErrCouldNotCreateDeviceNode = errors.New("could not create device node")
)

// expose a Silo Device as a file within the vm directory
func ExposeSiloDeviceAsFile(vmpath string, name string, devicePath string) error {
	if vmpath == "" {
		return nil
	}
	deviceInfo, err := os.Stat(devicePath)
	if err != nil {
		return errors.Join(ErrCouldNotGetBaseDeviceStat, err)
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
func CreateSiloDevSchema(i *MigrateFromDevice) (*config.DeviceSchema, error) {
	stat, err := os.Stat(i.Base)
	if err != nil {
		return nil, errors.Join(ErrCouldNotGetBaseDeviceStat, err)
	}

	ds := &config.DeviceSchema{
		Name:      i.Name,
		BlockSize: fmt.Sprintf("%v", i.BlockSize),
		Expose:    true,
		Size:      fmt.Sprintf("%v", stat.Size()),
		//		Binlog:    path.Join("binlog", i.Name),

	}

	if i.AnyOrder {
		ds.Migration = &config.MigrationConfigSchema{
			AnyOrder: true,
		}
	}

	if strings.TrimSpace(i.Overlay) == "" || strings.TrimSpace(i.State) == "" {
		err := os.MkdirAll(filepath.Dir(i.Base), os.ModePerm)
		if err != nil {
			return nil, errors.Join(ErrCouldNotCreateDeviceDirectory, err)
		}
		ds.System = "file"
		ds.Location = i.Base
	} else {
		err := os.MkdirAll(filepath.Dir(i.Overlay), os.ModePerm)
		if err != nil {
			return nil, errors.Join(ErrCouldNotCreateOverlayDirectory, err)
		}

		err = os.MkdirAll(filepath.Dir(i.State), os.ModePerm)
		if err != nil {
			return nil, errors.Join(ErrCouldNotCreateStateDirectory, err)
		}

		ds.System = "file"
		if i.UseSparseFile {
			ds.System = "sparsefile"
		}
		ds.Location = i.Overlay

		ds.ROSourceShared = i.SharedBase

		ds.ROSource = &config.DeviceSchema{
			Name:     i.State,
			System:   "file",
			Location: i.Base,
			Size:     fmt.Sprintf("%v", stat.Size()),
		}
	}

	if i.S3Sync {
		ds.Sync = &config.SyncS3Schema{
			AccessKey:       i.S3AccessKey,
			SecretKey:       i.S3SecretKey,
			Endpoint:        i.S3Endpoint,
			Secure:          i.S3Secure,
			Bucket:          i.S3Bucket,
			Prefix:          i.S3Prefix,
			GrabPrefix:      i.S3Prefix,
			AutoStart:       true,
			GrabConcurrency: i.S3Concurrency,
			Config: &config.SyncConfigSchema{
				OnlyDirty:   i.S3OnlyDirty,
				BlockShift:  i.S3BlockShift,
				MaxAge:      i.S3MaxAge,
				MinChanged:  i.S3MinChanged,
				Limit:       i.S3Limit,
				CheckPeriod: i.S3CheckPeriod,
				Concurrency: i.S3Concurrency,
			},
		}
		if ds.ROSourceShared {
			ds.Sync.Config.OnlyDirty = true
		}
	}

	// Enable writeCache for memory (WIP)
	if i.UseWriteCache {
		ds.WriteCache = &config.WriteCacheSchema{
			MinSize:     i.WriteCacheMin,
			MaxSize:     i.WriteCacheMax,
			FlushPeriod: "5m",
			//			BlockSize:   i.WriteCacheBlocksize,
		}
	}

	return ds, nil
}

func CreateIncomingSiloDevSchema(i *MigrateFromDevice, schema *config.DeviceSchema) (*config.DeviceSchema, error) {
	ds := &config.DeviceSchema{
		Name:      i.Name,
		BlockSize: schema.BlockSize, // fmt.Sprintf("%v", i.BlockSize),
		Expose:    true,
		Size:      schema.Size,
		Sync:      schema.Sync,
	}
	if i.AnyOrder {
		ds.Migration = &config.MigrationConfigSchema{
			AnyOrder: true,
		}
	}
	if schema.Sync != nil {
		ds.Sync.AutoStart = false
	}
	if strings.TrimSpace(i.Overlay) == "" || strings.TrimSpace(i.State) == "" {
		err := os.MkdirAll(filepath.Dir(i.Base), os.ModePerm)
		if err != nil {
			return nil, errors.Join(ErrCouldNotCreateDeviceDirectory, err)
		}
		ds.System = "file"
		ds.Location = i.Base
	} else {
		err := os.MkdirAll(filepath.Dir(i.Overlay), os.ModePerm)
		if err != nil {
			return nil, errors.Join(ErrCouldNotCreateOverlayDirectory, err)
		}

		err = os.MkdirAll(filepath.Dir(i.State), os.ModePerm)
		if err != nil {
			return nil, errors.Join(ErrCouldNotCreateStateDirectory, err)
		}

		ds.Location = i.Overlay
		ds.System = "file"
		if i.UseSparseFile {
			ds.System = "sparsefile"
		}

		// If it was shared with us, assume it's going to be shared with future migrations
		ds.ROSourceShared = i.SharedBase

		ds.ROSource = &config.DeviceSchema{
			Name:     i.State,
			System:   "file",
			Location: i.Base,
			Size:     schema.Size,
		}
	}
	return ds, nil
}

/**
 * 'migrate' from the local filesystem.
 *
 */
func MigrateFromFS(log types.Logger, met metrics.SiloMetrics, instanceID, vmpath string,
	devices []MigrateFromDevice, tweak func(index int, name string, schema *config.DeviceSchema) *config.DeviceSchema) (*devicegroup.DeviceGroup, error) {
	siloDeviceSchemas := make([]*config.DeviceSchema, 0)

	skipSilos := make(map[string]bool)
	for i, input := range devices {
		skipSilos[input.Name] = input.SkipSilo

		if input.Shared {
			// Deal with shared devices here...
			err := ExposeSiloDeviceAsFile(vmpath, input.Name, input.Base)
			if err != nil {
				return nil, err
			}
		} else {
			ds, err := CreateSiloDevSchema(&input)
			if err != nil {
				return nil, err
			}

			if tweak != nil {
				ds = tweak(i, input.Name, ds)
			}
			siloDeviceSchemas = append(siloDeviceSchemas, ds)
		}
	}

	var slog types.Logger
	if log != nil {
		slog = log.SubLogger("silo")
	}

	// Create a silo deviceGroup from all the schemas
	dg, err := devicegroup.NewFromSchema(instanceID, siloDeviceSchemas, false, slog, met)
	if err != nil {
		return nil, err
	}

	for _, input := range siloDeviceSchemas {

		if skipSilos[input.Name] {
			// Rather than putting Silo in the mix here, we just copy the data over.
			di := dg.GetDeviceInformationByName(input.Name)
			src, err := os.Open(filepath.Join("/dev", di.Exp.Device()))
			if err != nil {
				return nil, err
			}
			dst, err := os.OpenFile(path.Join(vmpath, input.Name), os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
			if err != nil {
				return nil, err
			}

			if log != nil {
				log.Info().Str("from", filepath.Join("/dev", di.Exp.Device())).Str("to", path.Join(vmpath, input.Name)).Msg("Copying")
			}

			n, err := io.Copy(dst, src)
			if err != nil {
				return nil, err
			}
			err = src.Close()
			if err != nil {
				return nil, err
			}
			err = dst.Close()
			if err != nil {
				return nil, err
			}
			if log != nil {
				log.Info().Str("name", input.Name).Int64("bytes", n).Msg("Copied file over. Skipping Silo.")
			}
		} else {
			dev := dg.GetExposedDeviceByName(input.Name)
			err = ExposeSiloDeviceAsFile(vmpath, input.Name, filepath.Join("/dev", dev.Device()))
			if err != nil {
				return nil, err
			}
			if log != nil {
				log.Info().
					Str("name", input.Name).
					Str("vmpath", vmpath).
					Str("device", dev.Device()).
					Msg("silo device exposed")
			}
		}
	}
	return dg, nil
}

/**
 * Migrate FROM a pipe
 * NB: You should call dg.WaitForCompletion() later to ensure migrations are finished
 */
func MigrateFromPipe(log types.Logger, met metrics.SiloMetrics, instanceID string, vmpath string,
	ctx context.Context, readers []io.Reader, writers []io.Writer, schemaTweak func(index int, name string, schema *config.DeviceSchema) *config.DeviceSchema,
	cdh func([]byte)) (*devicegroup.DeviceGroup, error) {
	ready := make(chan bool)

	protocolCtx, protocolCancel := context.WithCancel(ctx)

	pro := protocol.NewRW(protocolCtx, readers, writers, nil)

	// Add pro to metrics
	if met != nil {
		met.AddProtocol(instanceID, "migrateFromPipe", pro)
	}

	// Start a goroutine to do the protocol Handle()
	go func() {
		err := pro.Handle()
		if err != nil {
			// EOF is expected, but it shouldn't cancel the context. That should be done externally, once
			// everything is completed, including alternate sources grab.
			if err != io.EOF {
				if err != context.Canceled {
					// This deserves a warning log message, but will result in other errors being returned
					// so can be ignored here.
					if log != nil {
						log.Warn().Err(err).Msg("protocol handle error")
					}
				}

				protocolCancel()
			}
		}
	}()

	events := func(e *packets.Event) {}
	icdh := func(data []byte) {
		if cdh != nil {
			cdh(data)
		}
		close(ready)
	}

	var slog types.Logger
	if log != nil {
		slog = log.SubLogger("silo")
	}

	dg, err := devicegroup.NewFromProtocol(protocolCtx, instanceID, pro, schemaTweak, events, icdh, slog, met)
	if err != nil {
		return nil, err
	}

	// Expose all devices
	for _, n := range dg.GetAllNames() {
		dev := dg.GetExposedDeviceByName(n)
		if dev != nil {
			err := ExposeSiloDeviceAsFile(vmpath, n, filepath.Join("/dev", dev.Device()))
			if err != nil {
				return nil, err
			}
			if log != nil {
				log.Info().
					Str("name", n).
					Str("vmpath", vmpath).
					Str("device", dev.Device()).
					Msg("silo device exposed")
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

type MigrateToOptions struct {
	Concurrency     int
	Compression     bool
	CompressionType packets.CompressionType
}

/**
 * Migrate TO a pipe
 *
 */
func MigrateToPipe(ctx context.Context, log types.Logger, readers []io.Reader, writers []io.Writer,
	dg *devicegroup.DeviceGroup, options *MigrateToOptions, onProgress func(p map[string]*migrator.MigrationProgress),
	vmState *VMStateMgr, devices []MigrateToDevice, getCustomPayload func() []byte, met metrics.SiloMetrics, instanceID string) error {

	if log != nil {
		log.Info().Msg("MigrateToPipe")
	}

	protocolCtx, protocolCancel := context.WithCancel(ctx)

	// Create a protocol for use by Silo
	pro := protocol.NewRW(protocolCtx, readers, writers, nil)

	// Start a goroutine to do the protocol Handle()
	go func() {
		err := pro.Handle()
		if err != nil && err != io.EOF {
			protocolCancel()
		}
	}()

	// Add pro to metrics
	if met != nil {
		met.AddProtocol(instanceID, "migrateToPipe", pro)
	}

	// Start a migration to the protocol. This will send all schema info etc
	err := dg.StartMigrationTo(pro, options.Compression, options.CompressionType)
	if err != nil {
		return err
	}

	if log != nil {
		m := pro.GetMetrics()
		log.Info().
			Uint64("DataSent", m.DataSent).
			Uint64("DataRecv", m.DataRecv).
			Msg("MigrateToPipe.StartMigrationTo")
	}

	// Do the main migration of the data...
	err = dg.MigrateAll(options.Concurrency, onProgress)
	if err != nil {
		return err
	}

	if log != nil {
		m := pro.GetMetrics()
		log.Info().
			Uint64("DataSent", m.DataSent).
			Uint64("DataRecv", m.DataRecv).
			Msg("MigrateToPipe.MigrateAll")
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
		if getCustomPayload != nil {
			cdata := getCustomPayload()
			if cdata != nil {
				return dg.SendCustomData(cdata)
			}
		}
		return dg.SendCustomData([]byte{})
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

	if log != nil {
		m := pro.GetMetrics()
		log.Info().
			Uint64("DataSent", m.DataSent).
			Uint64("DataRecv", m.DataRecv).
			Msg("MigrateToPipe.MigrateDirty")
	}

	// Send Silo completion events for the devices. This will trigger any S3 sync behaviour etc.
	err = dg.Completed()
	if err != nil {
		return err
	}

	if log != nil {
		m := pro.GetMetrics()
		log.Info().
			Uint64("DataSent", m.DataSent).
			Uint64("DataRecv", m.DataRecv).
			Msg("MigrateToPipe.Completed")
	}

	return nil
}
