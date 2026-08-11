// Package config loads and validates process environment.
//
// Callers should invoke Load() exactly once at process start. In
// development we attempt to source a local `.env` file first so devs
// don't have to export vars manually; in production the file is
// absent and env.Parse pulls straight from the real environment.
package config

import (
	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

// Env is the fully-parsed process configuration. Every field is
// populated from an environment variable via struct tags; required
// fields cause Load to fail fast if unset.
type Env struct {
	Port              string `env:"PORT"              envDefault:"8080"`
	DatabaseURL       string `env:"DATABASE_URL,required"`
	SupabaseURL       string `env:"SUPABASE_URL,required"`
	SupabaseAnonKey   string `env:"SUPABASE_ANON_KEY,required"`
	SupabaseJWTSecret string `env:"SUPABASE_JWT_SECRET,required"`
	CorsOrigin        string `env:"CORS_ORIGIN,required"`
	Env               string `env:"ENV"               envDefault:"development"`
}

// Load reads the environment, optionally pre-populated from a local
// `.env` file. A missing `.env` is not an error — production runs
// depend on real env vars.
func Load() (Env, error) {
	_ = godotenv.Load()

	var e Env
	if err := env.Parse(&e); err != nil {
		return Env{}, err
	}
	return e, nil
}

// IsDevelopment reports whether the process is running in the
// development environment. Used to gate dev-only behaviour like
// verbose error responses.
func (e Env) IsDevelopment() bool {
	return e.Env == "development"
}
