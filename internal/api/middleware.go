package api

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// recoveryMiddleware converts a handler panic into a 500 response without
// terminating the process (CLAUDE.md §13 — never panic across the API boundary).
func (s *Server) recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture context before entering defer so contextcheck is satisfied.
		ctx := r.Context()
		defer func() {
			if rec := recover(); rec != nil {
				s.log.ErrorContext(ctx, "api: handler panic",
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())),
				)
				http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestLogMiddleware logs each request with method, path, and response status.
// Identity attributes are added if a scope is already on the context.
func (s *Server) requestLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &responseWriter{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rw, r)
		s.log.InfoContext(r.Context(), "api: request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rw.code),
		)
	})
}

// bodyLimitMiddleware caps the request body at s.maxBodyB bytes.
func (s *Server) bodyLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyB)
		next.ServeHTTP(w, r)
	})
}

// responseWriter wraps http.ResponseWriter to capture the status code for
// logging.
type responseWriter struct {
	http.ResponseWriter
	code int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.code = code
	rw.ResponseWriter.WriteHeader(code)
}
