package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// SupabaseJWKSPath is the well-known suffix under a Supabase
// project's auth endpoint that publishes the project's public JWK
// Set. Kept as a package constant so tests and the bootstrap agree
// on one location.
const SupabaseJWKSPath = "/auth/v1/.well-known/jwks.json"

// SupabaseIssuerSuffix is what Supabase Auth sets on the `iss`
// claim of every access token it mints — the project's base URL
// followed by /auth/v1. RequireAuth checks the claim against
// <SUPABASE_URL> + this suffix to prevent tokens from a different
// project (or a hostile issuer) from being accepted.
const SupabaseIssuerSuffix = "/auth/v1"

// SupabaseJWKS bundles the keyfunc used to verify signatures and
// the expected iss claim used to reject foreign tokens. Both are
// derived from SUPABASE_URL and constructed once at boot.
type SupabaseJWKS struct {
	// Keyfunc resolves the correct public key for a given JWT
	// header (specifically the `kid`) by consulting the cached
	// JWKS. It's safe for concurrent use.
	Keyfunc jwt.Keyfunc

	// ExpectedIssuer is `<SUPABASE_URL>/auth/v1`. RequireAuth
	// compares the token's `iss` claim against this exactly.
	ExpectedIssuer string
}

// NewSupabaseJWKS builds a SupabaseJWKS by fetching the project's
// JWK Set once at boot. keyfunc.NewDefaultCtx launches a background
// refresh goroutine that ends when ctx is cancelled — pass a
// context tied to process lifetime so the goroutine shuts down
// with the server.
//
// This function returns an error if the JWKS endpoint is
// unreachable at boot; fail fast is preferable to serving 401s
// under mystery conditions later.
//
// Requires that the Supabase project has asymmetric JWT signing
// enabled (Dashboard → Auth → Signing Keys → migrate to ES256/RS256).
// A project still on the legacy shared HS256 secret publishes an
// empty JWKS and every request will fail INVALID_TOKEN.
func NewSupabaseJWKS(ctx context.Context, supabaseURL string) (*SupabaseJWKS, error) {
	base := strings.TrimRight(supabaseURL, "/")
	jwksURL := base + SupabaseJWKSPath

	kf, err := keyfunc.NewDefaultCtx(ctx, []string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("bootstrap jwks from %s: %w", jwksURL, err)
	}

	return &SupabaseJWKS{
		Keyfunc:        kf.Keyfunc,
		ExpectedIssuer: base + SupabaseIssuerSuffix,
	}, nil
}
