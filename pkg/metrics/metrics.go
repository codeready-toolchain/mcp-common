package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (

	// MCPCallsTotal counts total MCP calls by method, name (for `tools/call`) and success
	MCPCallsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mcp_calls_total",
			Help: "Total number of MCP calls",
		},
		[]string{"server", "method", "name", "success"},
	)

	// MCPCallDuration measures the duration of MCP calls by method, name (for `tools/call`) and success
	MCPCallDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "mcp_call_duration_seconds",
			Help:    "Duration of MCP calls in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"server", "method", "name", "success"},
	)
)
