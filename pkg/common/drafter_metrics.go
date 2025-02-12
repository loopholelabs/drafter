package common

import "github.com/prometheus/client_golang/prometheus"

type DrafterMetrics struct {
	MetricFlushDataOps    *prometheus.GaugeVec
	MetricFlushDataTimeMS *prometheus.GaugeVec
	MetricVMRunning       *prometheus.GaugeVec
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
	}

	reg.MustRegister(dm.MetricFlushDataOps, dm.MetricFlushDataTimeMS, dm.MetricVMRunning)

	return dm
}
