package middleware_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/codeready-toolchain/mcp-common/pkg/middleware"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRecoveryMiddleware(t *testing.T) {
	t.Run("recovers nil deref panic and returns error", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
			var p *struct{ x int }
			return nil, func() error { _ = p.x; return nil }() //nolint:staticcheck // deliberate nil-deref
		}
		mw := middleware.NewRecoveryMiddleware(logger)
		handler := mw(next)

		req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{
			Session: &mcp.ServerSession{},
			Params:  &mcp.CallToolParamsRaw{Name: "crashy"},
		}
		result, err := handler(context.Background(), "tools/call", req)

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "panicked")
		logs := buf.String()
		assert.Contains(t, logs, "MCP method panicked")
		assert.Contains(t, logs, "tools/call")
	})

	t.Run("passes through on no panic", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
		expected := &mcp.CallToolResult{}
		next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
			return expected, nil
		}
		mw := middleware.NewRecoveryMiddleware(logger)
		handler := mw(next)

		req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{
			Session: &mcp.ServerSession{},
			Params:  &mcp.CallToolParamsRaw{Name: "happy"},
		}
		result, err := handler(context.Background(), "tools/call", req)

		require.NoError(t, err)
		assert.Equal(t, expected, result)
	})

	t.Run("nil logger does not itself panic", func(t *testing.T) {
		next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
			panic("boom")
		}
		mw := middleware.NewRecoveryMiddleware(nil)
		handler := mw(next)

		req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{
			Session: &mcp.ServerSession{},
			Params:  &mcp.CallToolParamsRaw{Name: "t"},
		}
		assert.NotPanics(t, func() {
			_, err := handler(context.Background(), "tools/call", req)
			assert.Error(t, err)
		})
	})
}
