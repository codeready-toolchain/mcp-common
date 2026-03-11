package middleware

import (
	"context"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func NewLoggingMiddleware(logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if ctr, ok := req.(*mcp.CallToolRequest); ok {
				logger.Info("MCP method started",
					"method", method,
					"session_id", ctr.GetSession().ID(),
					"name", ctr.Params.Name,
					"has_args", len(ctr.Params.Arguments) > 0)
			} else {
				logger.Info("MCP method started",
					"method", method,
					"session_id", req.GetSession().ID(),
					"has_params", req.GetParams() != nil,
				)
			}

			start := time.Now()
			result, err := next(ctx, method, req)
			duration := time.Since(start)

			if err != nil {
				logger.Error("MCP call failed",
					"method", method,
					"session_id", req.GetSession().ID(),
					"duration_ms", duration.Milliseconds(),
					"error", err.Error(),
				)
			} else {
				logger.Info("MCP call completed",
					"method", method,
					"session_id", req.GetSession().ID(),
					"duration_ms", duration.Milliseconds(),
					"has_result", result != nil,
				)
			}
			return result, err
		}
	}
}
