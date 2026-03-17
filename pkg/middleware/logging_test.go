package middleware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/codeready-toolchain/mcp-common/pkg/middleware"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLoggingMiddleware(t *testing.T) {
	t.Run("logs CallToolRequest with tool name on success", func(t *testing.T) {
		// given
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		nextCalled := false
		expectedResult := &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "result"}},
		}
		next := func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
			nextCalled = true
			return expectedResult, nil
		}

		req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{
			Session: &mcp.ServerSession{},
			Params: &mcp.CallToolParamsRaw{
				Name:      "my-tool",
				Arguments: json.RawMessage(`{"key":"value"}`),
			},
		}

		mw := middleware.NewLoggingMiddleware(logger)
		handler := mw(next)

		// when
		result, err := handler(context.Background(), "tools/call", req)

		// then
		require.NoError(t, err)
		assert.True(t, nextCalled)
		assert.Equal(t, expectedResult, result)

		logs := buf.String()
		assert.Contains(t, logs, "MCP method started")
		assert.Contains(t, logs, "my-tool")
		assert.Contains(t, logs, "MCP call completed")
		assert.Contains(t, logs, "tools/call")
		assert.NotContains(t, logs, "MCP call failed")
	})

	t.Run("logs CallToolRequest without arguments", func(t *testing.T) {
		// given
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		next := func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
			return &mcp.CallToolResult{}, nil
		}

		req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{
			Session: &mcp.ServerSession{},
			Params:  &mcp.CallToolParamsRaw{Name: "no-args-tool"},
		}

		handler := middleware.NewLoggingMiddleware(logger)(next)

		// when
		_, err := handler(context.Background(), "tools/call", req)

		// then
		require.NoError(t, err)

		logs := buf.String()
		assert.Contains(t, logs, "no-args-tool")
		assert.Contains(t, logs, `"has_args":false`)
	})

	t.Run("logs error when next handler returns error", func(t *testing.T) {
		// given
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		expectedErr := errors.New("something went wrong")
		next := func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
			return nil, expectedErr
		}

		req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{
			Session: &mcp.ServerSession{},
			Params:  &mcp.CallToolParamsRaw{Name: "failing-tool"},
		}

		handler := middleware.NewLoggingMiddleware(logger)(next)

		// when
		result, err := handler(context.Background(), "tools/call", req)

		// then
		assert.Nil(t, result)
		require.ErrorIs(t, err, expectedErr)

		logs := buf.String()
		assert.Contains(t, logs, "MCP call failed")
		assert.Contains(t, logs, "something went wrong")
		assert.NotContains(t, logs, "MCP call completed")
	})

	t.Run("logs non-CallToolRequest with has_params", func(t *testing.T) {
		// given
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		next := func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
			return nil, nil
		}

		req := &mcp.ServerRequest[*mcp.ListToolsParams]{
			Session: &mcp.ServerSession{},
			Params:  &mcp.ListToolsParams{},
		}

		handler := middleware.NewLoggingMiddleware(logger)(next)

		// when
		_, err := handler(context.Background(), "tools/list", req)

		// then
		require.NoError(t, err)

		logs := buf.String()
		assert.Contains(t, logs, "MCP method started")
		assert.Contains(t, logs, "tools/list")
		assert.Contains(t, logs, "has_params")
		assert.NotContains(t, logs, "MCP call failed")
	})

	t.Run("next handler result is passed through", func(t *testing.T) {
		// given
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, nil))

		expectedResult := &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "hello"}},
			IsError: false,
		}
		next := func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
			return expectedResult, nil
		}

		req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{
			Session: &mcp.ServerSession{},
			Params:  &mcp.CallToolParamsRaw{Name: "pass-through-tool"},
		}

		handler := middleware.NewLoggingMiddleware(logger)(next)

		// when
		result, err := handler(context.Background(), "tools/call", req)

		// then
		require.NoError(t, err)
		assert.Equal(t, expectedResult, result)
	})
}
