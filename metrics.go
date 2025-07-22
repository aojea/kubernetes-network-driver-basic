package main

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var registerMetricsOnce sync.Once

var (
	nodePrepareRequestsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "kubernetes-networking",
		Subsystem: "driver",
		Name:      "node_prepare_requests_total",
		Help:      "Total number of NodePrepareResources requests received.",
	})
)

// IMPORTANT: register the metrics here
func registerMetrics() {
	registerMetricsOnce.Do(func() {
		prometheus.MustRegister(nodePrepareRequestsTotal)
	})
}
