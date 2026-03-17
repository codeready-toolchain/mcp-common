package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/codeready-toolchain/mcp-common/pkg/metrics"
	"github.com/codeready-toolchain/mcp-common/pkg/middleware"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMetricsMiddleware(t *testing.T) {
	logger := slog.Default()

	t.Run("increments counter on successful tool call", func(t *testing.T) {
		// given
		counter := metrics.MCPCallsTotal.WithLabelValues("test-server", "tools/call", "my-tool", "true")
		before := testutil.ToFloat64(counter)

		next := func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "ok"}},
			}, nil
		}

		req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{
			Session: &mcp.ServerSession{},
			Params: &mcp.CallToolParamsRaw{
				Name:      "my-tool",
				Arguments: json.RawMessage(`{}`),
			},
		}

		handler := middleware.NewMetricsMiddleware("test-server", logger)(next)

		// when
		result, err := handler(context.Background(), "tools/call", req)

		// then
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.InDelta(t, float64(1), testutil.ToFloat64(counter)-before, 0)
	})

	t.Run("records failure when next handler returns error", func(t *testing.T) {
		// given
		failCounter := metrics.MCPCallsTotal.WithLabelValues("test-server", "tools/call", "error-tool", "false")
		before := testutil.ToFloat64(failCounter)

		next := func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
			return nil, errors.New("handler error")
		}

		req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{
			Session: &mcp.ServerSession{},
			Params: &mcp.CallToolParamsRaw{
				Name: "error-tool",
			},
		}

		handler := middleware.NewMetricsMiddleware("test-server", logger)(next)

		// when
		_, err := handler(context.Background(), "tools/call", req)

		// then
		require.Error(t, err)
		assert.InDelta(t, float64(1), testutil.ToFloat64(failCounter)-before, 0)
	})

	t.Run("records failure when CallToolResult has IsError true", func(t *testing.T) {
		// given
		failCounter := metrics.MCPCallsTotal.WithLabelValues("test-server", "tools/call", "is-error-tool", "false")
		before := testutil.ToFloat64(failCounter)

		next := func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "error output"}},
				IsError: true,
			}, nil
		}

		req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{
			Session: &mcp.ServerSession{},
			Params: &mcp.CallToolParamsRaw{
				Name: "is-error-tool",
			},
		}

		handler := middleware.NewMetricsMiddleware("test-server", logger)(next)

		// when
		result, err := handler(context.Background(), "tools/call", req)

		// then
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.InDelta(t, float64(1), testutil.ToFloat64(failCounter)-before, 0)
	})

	t.Run("records duration histogram", func(t *testing.T) {
		// given
		next := func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "ok"}},
			}, nil
		}

		req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{
			Session: &mcp.ServerSession{},
			Params: &mcp.CallToolParamsRaw{
				Name: "duration-tool",
			},
		}

		handler := middleware.NewMetricsMiddleware("test-server", logger)(next)

		// when
		_, err := handler(context.Background(), "tools/call", req)

		// then
		require.NoError(t, err)
		histogram := metrics.MCPCallDuration.WithLabelValues("test-server", "tools/call", "duration-tool", "true")
		assert.NotNil(t, histogram)
	})

	t.Run("handles non-tool-call method with empty tool name", func(t *testing.T) {
		// given
		counter := metrics.MCPCallsTotal.WithLabelValues("test-server", "tools/list", "", "true")
		before := testutil.ToFloat64(counter)

		next := func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
			return nil, nil
		}

		req := &mcp.ServerRequest[*mcp.ListToolsParams]{
			Session: &mcp.ServerSession{},
			Params:  &mcp.ListToolsParams{},
		}

		handler := middleware.NewMetricsMiddleware("test-server", logger)(next)

		// when
		_, err := handler(context.Background(), "tools/list", req)

		// then
		require.NoError(t, err)
		assert.InDelta(t, float64(1), testutil.ToFloat64(counter)-before, 0)
	})

	t.Run("uses correct server name in metrics labels", func(t *testing.T) {
		// given
		counter := metrics.MCPCallsTotal.WithLabelValues("custom-server", "tools/call", "label-tool", "true")
		before := testutil.ToFloat64(counter)

		next := func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "ok"}},
			}, nil
		}

		req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{
			Session: &mcp.ServerSession{},
			Params:  &mcp.CallToolParamsRaw{Name: "label-tool"},
		}

		handler := middleware.NewMetricsMiddleware("custom-server", logger)(next)

		// when
		_, err := handler(context.Background(), "tools/call", req)

		// then
		require.NoError(t, err)
		assert.InDelta(t, float64(1), testutil.ToFloat64(counter)-before, 0)
	})
}
