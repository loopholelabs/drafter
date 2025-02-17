package common

import "github.com/prometheus/client_golang/prometheus"

type DrafterMetrics struct {
	MetricFlushDataOps           *prometheus.GaugeVec // Count of flushData operations
	MetricFlushDataTimeMS        *prometheus.GaugeVec // Total time for flushData operations
	MetricVMRunning              *prometheus.GaugeVec // 1 = VM is running
	MetricMigratingTo            *prometheus.GaugeVec // 1 = Migrating to
	MetricMigratingFrom          *prometheus.GaugeVec // 1 = Migrating from
	MetricMigratingFromWaitReady *prometheus.GaugeVec // 1 = Migrating from
}

func NewDrafterMetrics(reg *prometheus.Registry) *DrafterMetrics {
	labels := []string{"instance_id"}

	dm := &DrafterMetrics{
		MetricFlushDataOps: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "drafter", Subsystem: "peer", Name: "flush_data_ops", Help: "Flush data ops"}, labels),
		MetricFlushDataTimeMS: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "drafter", Subsystem: "peer", Name: "flush_data_time_ms", Help: "Flush data time ms"}, labels),
		MetricVMRunning: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "drafter", Subsystem: "peer", Name: "vm_running", Help: "vm running"}, labels),
		MetricMigratingTo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "drafter", Subsystem: "peer", Name: "migrating_to", Help: "migrating to"}, labels),
		MetricMigratingFrom: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "drafter", Subsystem: "peer", Name: "migrating_from", Help: "migrating from"}, labels),
		MetricMigratingFromWaitReady: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "drafter", Subsystem: "peer", Name: "migrating_from_wait_ready", Help: "migrating from wait ready"}, labels),
	}

	reg.MustRegister(dm.MetricFlushDataOps, dm.MetricFlushDataTimeMS, dm.MetricVMRunning,
		dm.MetricMigratingTo, dm.MetricMigratingFrom, dm.MetricMigratingFromWaitReady)

	return dm
}
