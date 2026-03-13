package metrics_test

import (
	"testing"

	"github.com/codeready-toolchain/mcp-common/pkg/metrics"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPCallsTotal(t *testing.T) {
	t.Run("counter is registered and can be incremented", func(t *testing.T) {
		// given
		require.NotNil(t, metrics.MCPCallsTotal)
		counter := metrics.MCPCallsTotal.WithLabelValues("test-server", "tools/call", "my-tool", "true")
		before := testutil.ToFloat64(counter)

		// when
		counter.Inc()

		// then
		after := testutil.ToFloat64(counter)
		assert.InDelta(t, float64(1), after-before, 0)
	})

	t.Run("counter tracks different label combinations independently", func(t *testing.T) {
		// given
		require.NotNil(t, metrics.MCPCallsTotal)
		successCounter := metrics.MCPCallsTotal.WithLabelValues("test-server", "tools/call", "tool-a", "true")
		failureCounter := metrics.MCPCallsTotal.WithLabelValues("test-server", "tools/call", "tool-a", "false")

		beforeSuccess := testutil.ToFloat64(successCounter)
		beforeFailure := testutil.ToFloat64(failureCounter)

		// when
		successCounter.Inc()
		successCounter.Inc()
		failureCounter.Inc()

		// then
		assert.InDelta(t, float64(2), testutil.ToFloat64(successCounter)-beforeSuccess, 0)
		assert.InDelta(t, float64(1), testutil.ToFloat64(failureCounter)-beforeFailure, 0)
	})
}

func TestMCPCallDuration(t *testing.T) {
	t.Run("histogram is registered and can observe values", func(t *testing.T) {
		// given
		require.NotNil(t, metrics.MCPCallDuration)
		histogram := metrics.MCPCallDuration.WithLabelValues("test-server", "tools/call", "my-tool", "true")

		// when
		histogram.Observe(0.5)
		histogram.Observe(1.0)

		// then (no panic means histogram is working)
		assert.NotNil(t, histogram)
	})
}
