package middleware

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/codeready-toolchain/mcp-common/pkg/metrics"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func NewMetricsMiddleware(serverName string, logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			logger.Debug("metrics-middleware: received request", "method", method, "params", req.GetParams())
			start := time.Now()
			result, err := next(ctx, method, req)
			duration := time.Since(start)
			var tool string
			if p, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok {
				tool = p.Name
			}
			success := err == nil
			if r, ok := result.(*mcp.CallToolResult); ok {
				logger.Debug("metrics-middleware: call tool result", "is-error", r.IsError)
				success = success && !r.IsError
			}
			metrics.MCPCallsTotal.WithLabelValues(serverName, method, tool, strconv.FormatBool(success)).Inc()
			metrics.MCPCallDuration.WithLabelValues(serverName, method, tool, strconv.FormatBool(success)).Observe(duration.Seconds())
			return result, err
		}
	}
}
