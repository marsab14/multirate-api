// Package app assembles the HTTP router: middleware stack, CORS,
// health probe, and (in later batches) the /api/* routes. It exposes
// a single New() constructor so main can wire it into an http.Server.
package app

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jmoiron/sqlx"

	"billing-api/internal/auth"
	"billing-api/internal/config"
	"billing-api/internal/handlers"
)

// Deps bundles everything a route handler might need. Kept as a
// struct (rather than package-level globals) so the router is easy
// to test with fakes.
type Deps struct {
	Env    config.Env
	DB     *sqlx.DB
	Logger *slog.Logger
}

// New builds the chi.Mux with the full middleware stack and mounts
// every route the app currently exposes. Called once from main.
func New(d Deps) *chi.Mux {
	r := chi.NewMux()

	sbClient := auth.NewSupabaseClient(d.Env.SupabaseURL, d.Env.SupabaseAnonKey)
	authHandlers := handlers.NewAuthHandlers(sbClient)
	docHandlers := handlers.NewDocumentHandlers(d.DB)
	lineHandlers := handlers.NewLineHandlers(d.DB)
	reportHandlers := handlers.NewReportHandlers(d.DB)

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(d.Logger))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   splitAndTrim(d.Env.CorsOrigin),
		AllowedMethods:   []string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))
	r.Use(middleware.SetHeader("Content-Type", "application/json"))

	r.Get("/health", healthHandler)

	// Auth routes are proxies to Supabase and MUST NOT sit behind
	// RequireAuth — callers hitting /login won't have a token yet.
	// Everything else under /api is gated behind a token check via
	// chi's r.Group so the middleware wraps only the routes defined
	// inside the group closure (not the sibling /api/auth subtree).
	r.Route("/api", func(r chi.Router) {
		r.Route("/auth", authHandlers.Mount)

		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAuth(d.Env.SupabaseJWTSecret))
			r.Route("/documents", func(r chi.Router) {
				docHandlers.Mount(r)
				r.Route("/{id}/lines", lineHandlers.Mount)
			})
			r.Route("/reports", reportHandlers.Mount)
		})
	})

	return r
}

// healthHandler is intentionally unauthenticated — it exists so
// container orchestrators and uptime probes can hit the process
// without carrying a JWT.
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// requestLogger emits one structured line per request with method,
// path, status, duration, and the chi request ID for correlation.
func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			logger.LogAttrs(r.Context(), slog.LevelInfo, "http_request",
				slog.String("request_id", middleware.GetReqID(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Duration("duration", time.Since(start)),
			)
		})
	}
}

// splitAndTrim turns "a,b , c" into []string{"a","b","c"}. Empty
// segments are dropped so a trailing comma doesn't produce a "*"
// origin by accident.
func splitAndTrim(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}
