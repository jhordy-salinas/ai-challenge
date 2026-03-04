package middleware

import (
	"log/slog"
	"net/http"
	"time"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
)

// statusWriter captures the response status code.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

// RequestLogger returns middleware that logs each request as structured JSON.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(sw, r)

			logger.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration_ms", float64(time.Since(start).Nanoseconds())/1e6,
				"remote_addr", r.RemoteAddr,
			)
		})
	}
}

// RequestIDHeader copies chi's request ID into the X-Request-Id response header
// so clients can correlate requests to server logs.
func RequestIDHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reqID := r.Context().Value(chiMiddleware.RequestIDKey); reqID != nil {
			if id, ok := reqID.(string); ok {
				w.Header().Set("X-Request-Id", id)
			}
		}
		next.ServeHTTP(w, r)
	})
}
