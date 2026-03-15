package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

type contextKey string

const requestIDKey contextKey = "request_id"
const loggerKey contextKey = "ctx_logger"

// LoggerFromCtx returns the request-scoped logger (with request_id baked in).
func LoggerFromCtx(ctx context.Context, fallback *zap.Logger) *zap.Logger {
	if l, ok := ctx.Value(loggerKey).(*zap.Logger); ok {
		return l
	}
	return fallback
}

func Logger(logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			// Capture request ID from upstream API (forwarded as X-Request-Id)
			// or fall back to chi's generated one.
			rid := r.Header.Get("X-Request-Id")
			if rid == "" {
				rid = middleware.GetReqID(r.Context())
			}

			ctx := r.Context()
			if rid != "" {
				ctx = context.WithValue(ctx, requestIDKey, rid)
				reqLogger := logger.With(zap.String("request_id", rid))
				ctx = context.WithValue(ctx, loggerKey, reqLogger)
			}

			next.ServeHTTP(ww, r.WithContext(ctx))

			fields := []zap.Field{
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", ww.Status()),
				zap.Duration("duration", time.Since(start)),
				zap.String("remote", r.RemoteAddr),
			}
			if rid != "" {
				fields = append(fields, zap.String("request_id", rid))
			}

			logger.Info("request", fields...)
		})
	}
}
