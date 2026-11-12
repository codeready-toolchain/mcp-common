package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewRecoveryMiddleware returns an mcp.Middleware that catches panics in
// downstream method handlers, logs the panic value and stack trace via the
// provided logger, and returns a structured MCP error result. Without this
// a single misbehaving tool (nil-deref, index out of range, …) crashes the
// entire MCP server process and takes every other tool with it.
func NewRecoveryMiddleware(logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (result mcp.Result, err error) {
			defer func() {
				r := recover()
				if r == nil {
					return
				}
				stack := debug.Stack()
				sessionID := ""
				if s := req.GetSession(); s != nil {
					sessionID = s.ID()
				}
				if logger != nil {
					logger.Error("MCP method panicked",
						"method", method,
						"session_id", sessionID,
						"panic", fmt.Sprint(r),
						"stack", string(stack),
					)
				}
				result = nil
				err = fmt.Errorf("mcp method %q panicked: %v", method, r)
			}()
			return next(ctx, method, req)
		}
	}
}
