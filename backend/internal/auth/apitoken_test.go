package auth

import (
	"context"
	"testing"
	"time"
)

// TestMintApiToken_RawShownOnceHashStored verifies mint returns the raw token once,
// persists only its hash (never the raw value), and that the raw resolves back to
// the right runner/scope identity.
func TestMintApiToken_RawShownOnceHashStored(t *testing.T) {
	ctx := context.Background()
	svc, client := newTestServiceCfg(t, Config{})
	u, err := svc.upsertUser(ctx, googleClaims{sub: "s", email: "runner@x.com"})
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	raw, tok, err := svc.MintApiToken(ctx, u.ID, "laptop CC", "laptop", []string{"digest.llm"}, nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if raw == "" {
		t.Fatal("mint returned an empty raw token")
	}

	stored := client.ApiToken.GetX(ctx, tok.ID)
	if stored.TokenHash == raw {
		t.Error("raw token was stored verbatim; only the hash must be persisted")
	}
	if stored.TokenHash != hashToken(raw) {
		t.Error("stored hash does not equal hashToken(raw)")
	}

	id, err := svc.AuthenticateBearer(ctx, raw)
	if err != nil || id == nil {
		t.Fatalf("AuthenticateBearer(valid) = (%v, %v), want an identity", id, err)
	}
	if id.Runner != "laptop" || id.TokenID != tok.ID || id.User == nil || id.User.ID != u.ID {
		t.Errorf("identity = %+v, want runner=laptop tokenID=%d owner=%d", id, tok.ID, u.ID)
	}
	if !id.Allows("digest.llm") || id.Allows("other.job") {
		t.Errorf("scope: Allows(digest.llm)=%v Allows(other.job)=%v, want true/false", id.Allows("digest.llm"), id.Allows("other.job"))
	}
}

// TestAuthenticateBearer_RevokedReturnsNil is the revoked-token invariant: after
// RevokeApiToken the raw no longer resolves (the middleware then 401s). Revoke is
// idempotent.
func TestAuthenticateBearer_RevokedReturnsNil(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestServiceCfg(t, Config{})
	u, _ := svc.upsertUser(ctx, googleClaims{sub: "s", email: "r@x.com"})
	raw, tok, err := svc.MintApiToken(ctx, u.ID, "n", "laptop", []string{"digest.llm"}, nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	if err := svc.RevokeApiToken(ctx, tok.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	id, err := svc.AuthenticateBearer(ctx, raw)
	if err != nil {
		t.Fatalf("authenticate after revoke: %v", err)
	}
	if id != nil {
		t.Error("revoked token still resolves to an identity")
	}
	if err := svc.RevokeApiToken(ctx, tok.ID); err != nil {
		t.Errorf("second revoke should be a no-op, got %v", err)
	}
}

// TestAuthenticateBearer_ExpiredReturnsNil is the expired-token invariant: a token
// past its expiry resolves to nothing, exactly like an expired session.
func TestAuthenticateBearer_ExpiredReturnsNil(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestServiceCfg(t, Config{})
	u, _ := svc.upsertUser(ctx, googleClaims{sub: "s", email: "r@x.com"})
	past := time.Now().Add(-time.Minute)
	raw, _, err := svc.MintApiToken(ctx, u.ID, "n", "laptop", []string{"digest.llm"}, &past)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	id, err := svc.AuthenticateBearer(ctx, raw)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if id != nil {
		t.Error("expired token still resolves to an identity")
	}
}

// TestAuthenticateBearer_UnknownReturnsNil: a token that was never minted resolves
// to nothing rather than erroring.
func TestAuthenticateBearer_UnknownReturnsNil(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestServiceCfg(t, Config{})
	id, err := svc.AuthenticateBearer(ctx, "not-a-real-token")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if id != nil {
		t.Error("unknown token resolved to an identity")
	}
}

// TestTouchApiToken_BumpsLastUsedAt: a freshly minted token has a null last_used_at
// that Touch stamps.
func TestTouchApiToken_BumpsLastUsedAt(t *testing.T) {
	ctx := context.Background()
	svc, client := newTestServiceCfg(t, Config{})
	u, _ := svc.upsertUser(ctx, googleClaims{sub: "s", email: "r@x.com"})
	_, tok, err := svc.MintApiToken(ctx, u.ID, "n", "laptop", []string{"digest.llm"}, nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if tok.LastUsedAt != nil {
		t.Error("freshly minted token should have a nil last_used_at")
	}
	if err := svc.TouchApiToken(ctx, tok.ID); err != nil {
		t.Fatalf("touch: %v", err)
	}
	if client.ApiToken.GetX(ctx, tok.ID).LastUsedAt == nil {
		t.Error("last_used_at not stamped after Touch")
	}
}

// TestBearerIdentity_EmptyScopeDeniesAll is the deny-by-default invariant at the
// unit level: an empty (or nil) scope authorizes nothing.
func TestBearerIdentity_EmptyScopeDeniesAll(t *testing.T) {
	empty := &BearerIdentity{Scope: []string{}}
	if empty.Allows("digest.llm") {
		t.Error("empty scope must authorize nothing")
	}
	var nilID *BearerIdentity
	if nilID.Allows("digest.llm") {
		t.Error("nil identity must authorize nothing")
	}
	scoped := &BearerIdentity{Scope: []string{"digest.llm"}}
	if !scoped.Allows("digest.llm") || scoped.Allows("finance.scrape") {
		t.Error("scoped identity must allow only its listed keys")
	}
}
