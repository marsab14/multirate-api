// Package auth verifies Supabase-issued JWTs and exposes the caller
// identity via request context. Handlers pull the current user from
// context rather than trusting anything in the request body.
//
// Verifier + context helpers land in B4.
package auth
