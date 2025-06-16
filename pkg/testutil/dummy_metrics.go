package testutil

import (
	"fmt"
	"sync"

	"github.com/loopholelabs/silo/pkg/storage/dirtytracker"
	"github.com/loopholelabs/silo/pkg/storage/expose"
	"github.com/loopholelabs/silo/pkg/storage/migrator"
	"github.com/loopholelabs/silo/pkg/storage/modules"
	"github.com/loopholelabs/silo/pkg/storage/protocol"
	"github.com/loopholelabs/silo/pkg/storage/sources"
	"github.com/loopholelabs/silo/pkg/storage/volatilitymonitor"
	"github.com/loopholelabs/silo/pkg/storage/waitingcache"
)

type DummyMetrics struct {
	lock             sync.Mutex
	metricsById      map[string]map[string]*modules.Metrics
	cowById          map[string]map[string]*modules.CopyOnWrite
	syncerById       map[string]map[string]*migrator.Syncer
	s3ById           map[string]map[string]*sources.S3Storage
	protocolById     map[string]map[string]*protocol.RW
	toProtocolById   map[string]map[string]*protocol.ToProtocol
	fromProtocolById map[string]map[string]*protocol.FromProtocol
}

func NewDummyMetrics() *DummyMetrics {
	return &DummyMetrics{
		metricsById:      make(map[string]map[string]*modules.Metrics),
		cowById:          make(map[string]map[string]*modules.CopyOnWrite),
		syncerById:       make(map[string]map[string]*migrator.Syncer),
		s3ById:           make(map[string]map[string]*sources.S3Storage),
		protocolById:     make(map[string]map[string]*protocol.RW),
		toProtocolById:   make(map[string]map[string]*protocol.ToProtocol),
		fromProtocolById: make(map[string]map[string]*protocol.FromProtocol),
	}
}

func (dm *DummyMetrics) GetMetrics(id string, name string) *modules.Metrics {
	dm.lock.Lock()
	defer dm.lock.Unlock()
	byid, ok := dm.metricsById[id]
	if !ok {
		return nil
	}
	return byid[name]
}

func (dm *DummyMetrics) GetCow(id string, name string) *modules.CopyOnWrite {
	dm.lock.Lock()
	defer dm.lock.Unlock()
	byid, ok := dm.cowById[id]
	if !ok {
		return nil
	}
	return byid[name]
}

func (dm *DummyMetrics) GetSyncer(id string, name string) *migrator.Syncer {
	dm.lock.Lock()
	defer dm.lock.Unlock()
	byid, ok := dm.syncerById[id]
	if !ok {
		return nil
	}
	return byid[name]
}

func (dm *DummyMetrics) GetS3Storage(id string, name string) *sources.S3Storage {
	dm.lock.Lock()
	defer dm.lock.Unlock()
	byid, ok := dm.s3ById[id]
	if !ok {
		return nil
	}
	return byid[name]
}

func (dm *DummyMetrics) GetProtocol(id string, name string) *protocol.RW {
	dm.lock.Lock()
	defer dm.lock.Unlock()
	byid, ok := dm.protocolById[id]
	if !ok {
		return nil
	}
	return byid[name]
}

func (dm *DummyMetrics) GetFromProtocol(id string, name string) *protocol.FromProtocol {
	dm.lock.Lock()
	defer dm.lock.Unlock()
	byid, ok := dm.fromProtocolById[id]
	if !ok {
		return nil
	}
	return byid[name]
}

func (dm *DummyMetrics) GetToProtocol(id string, name string) *protocol.ToProtocol {
	dm.lock.Lock()
	defer dm.lock.Unlock()
	byid, ok := dm.toProtocolById[id]
	if !ok {
		return nil
	}
	return byid[name]
}

func (dm *DummyMetrics) Shutdown()             {}
func (dm *DummyMetrics) RemoveAllID(id string) {}

func (dm *DummyMetrics) AddSyncer(id string, name string, sync *migrator.Syncer) {
	dm.lock.Lock()
	defer dm.lock.Unlock()
	_, ok := dm.syncerById[id]
	if !ok {
		dm.syncerById[id] = make(map[string]*migrator.Syncer)
	}
	dm.syncerById[id][name] = sync
}

func (dm *DummyMetrics) RemoveSyncer(id string, name string) {}

func (dm *DummyMetrics) AddMigrator(id string, name string, mig *migrator.Migrator) {}
func (dm *DummyMetrics) RemoveMigrator(id string, name string)                      {}

func (dm *DummyMetrics) AddProtocol(id string, name string, proto *protocol.RW) {
	fmt.Printf("#DM# AddProtocol %s %s\n", id, name)
	dm.lock.Lock()
	defer dm.lock.Unlock()
	_, ok := dm.protocolById[id]
	if !ok {
		dm.protocolById[id] = make(map[string]*protocol.RW)
	}
	dm.protocolById[id][name] = proto
}
func (dm *DummyMetrics) RemoveProtocol(id string, name string) {}

func (dm *DummyMetrics) AddToProtocol(id string, name string, proto *protocol.ToProtocol) {
	fmt.Printf("#DM# AddToProtocol %s %s\n", id, name)
	dm.lock.Lock()
	defer dm.lock.Unlock()
	_, ok := dm.toProtocolById[id]
	if !ok {
		dm.toProtocolById[id] = make(map[string]*protocol.ToProtocol)
	}
	dm.toProtocolById[id][name] = proto
}
func (dm *DummyMetrics) RemoveToProtocol(id string, name string) {}

func (dm *DummyMetrics) AddFromProtocol(id string, name string, proto *protocol.FromProtocol) {
	fmt.Printf("#DM# AddFromProtocol %s %s\n", id, name)
	dm.lock.Lock()
	defer dm.lock.Unlock()
	_, ok := dm.fromProtocolById[id]
	if !ok {
		dm.fromProtocolById[id] = make(map[string]*protocol.FromProtocol)
	}
	dm.fromProtocolById[id][name] = proto
}
func (dm *DummyMetrics) RemoveFromProtocol(id string, name string) {}

func (dm *DummyMetrics) AddS3Storage(id string, name string, s3 *sources.S3Storage) {
	fmt.Printf("#DM# AddS3Storage %s %s\n", id, name)
	dm.lock.Lock()
	defer dm.lock.Unlock()
	_, ok := dm.s3ById[id]
	if !ok {
		dm.s3ById[id] = make(map[string]*sources.S3Storage)
	}
	dm.s3ById[id][name] = s3
}
func (dm *DummyMetrics) RemoveS3Storage(id string, name string) {}

func (dm *DummyMetrics) AddDirtyTracker(id string, name string, dt *dirtytracker.Remote) {}
func (dm *DummyMetrics) RemoveDirtyTracker(id string, name string)                       {}

func (dm *DummyMetrics) AddVolatilityMonitor(id string, name string, vm *volatilitymonitor.VolatilityMonitor) {
}
func (dm *DummyMetrics) RemoveVolatilityMonitor(id string, name string) {}

func (dm *DummyMetrics) AddMetrics(id string, name string, mm *modules.Metrics) {
	fmt.Printf("#DM# AddMetrics %s %s\n", id, name)
	dm.lock.Lock()
	defer dm.lock.Unlock()
	_, ok := dm.metricsById[id]
	if !ok {
		dm.metricsById[id] = make(map[string]*modules.Metrics)
	}
	dm.metricsById[id][name] = mm
}
func (dm *DummyMetrics) RemoveMetrics(id string, name string) {}

func (dm *DummyMetrics) AddNBD(id string, name string, mm *expose.ExposedStorageNBDNL) {}
func (dm *DummyMetrics) RemoveNBD(id string, name string)                              {}

func (dm *DummyMetrics) AddWaitingCache(id string, name string, wc *waitingcache.Remote) {}
func (dm *DummyMetrics) RemoveWaitingCache(id string, name string)                       {}

func (dm *DummyMetrics) AddCopyOnWrite(id string, name string, cow *modules.CopyOnWrite) {
	fmt.Printf("#DM# AddCopyOnWrite %s %s\n", id, name)
	dm.lock.Lock()
	defer dm.lock.Unlock()
	_, ok := dm.cowById[id]
	if !ok {
		dm.cowById[id] = make(map[string]*modules.CopyOnWrite)
	}
	dm.cowById[id][name] = cow
}
func (dm *DummyMetrics) RemoveCopyOnWrite(id string, name string) {}
