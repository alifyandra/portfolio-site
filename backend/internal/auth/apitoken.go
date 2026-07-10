package auth

import (
	"context"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/apitoken"
)

// BearerIdentity is what an Authorization: Bearer token resolves to on the work
// API (ADR 0014). It is deliberately kept OUT of the user context slot: the admin
// and friend gates read UserFromContext (userCtxKey), which a bearer request never
// sets, so a runner token is invisible to them and can reach only the scope-gated
// work API. A leaked runner credential can therefore claim and complete the work
// its scope names and nothing else.
type BearerIdentity struct {
	// User is the token's owning User, for owner attribution on completed work.
	User *ent.User
	// Runner is the named runner identity, e.g. "laptop", "home-finance". It is the
	// value stamped as claimed_by / consumed-by runner, and tokens are independently
	// revocable per runner.
	Runner string
	// TokenID is the ApiToken row id, used to bump last_used_at and to revoke by id.
	TokenID int
	// Scope is the set of ScheduledJob keys this token may claim/complete work for.
	// An empty scope authorizes NOTHING.
	Scope []string
}

// Allows reports whether this identity's scope authorizes work for jobKey. An
// empty scope authorizes nothing (deny by default). Match is exact: there is no
// wildcard, so a token is scoped to precisely the job keys it names. See ADR 0014.
func (b *BearerIdentity) Allows(jobKey string) bool {
	if b == nil || jobKey == "" {
		return false
	}
	for _, s := range b.Scope {
		if s == jobKey {
			return true
		}
	}
	return false
}

// AuthenticateBearer resolves a raw bearer token to a BearerIdentity. An empty,
// unknown, revoked (deleted), expired, or orphaned token returns (nil, nil): the
// caller treats that as unauthenticated, not an error, mirroring Authenticate for
// sessions. Only the token hash is compared, so the raw token is never stored.
func (s *Service) AuthenticateBearer(ctx context.Context, rawToken string) (*BearerIdentity, error) {
	if rawToken == "" || s.ent == nil {
		return nil, nil
	}
	tok, err := s.ent.ApiToken.Query().
		Where(apitoken.TokenHash(hashToken(rawToken))).
		WithOwner().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil // unknown or revoked
		}
		return nil, err
	}
	// Expired tokens are treated as absent, exactly like an expired session.
	if tok.ExpiresAt != nil && !tok.ExpiresAt.After(time.Now()) {
		return nil, nil
	}
	owner := tok.Edges.Owner
	if owner == nil {
		return nil, nil // orphaned token (owner deleted): unauthenticated
	}
	return &BearerIdentity{
		User:    owner,
		Runner:  tok.Runner,
		TokenID: tok.ID,
		Scope:   append([]string(nil), tok.Scope...),
	}, nil
}

// MintApiToken creates a scope-only bearer token for an external runner, owned by
// userID. It returns the RAW token exactly ONCE (only its SHA-256 hash is stored),
// so the caller must surface it to the operator immediately; it can never be
// recovered afterwards. expiresAt is optional (nil = non-expiring). See ADR 0014.
func (s *Service) MintApiToken(ctx context.Context, userID int, name, runner string, scope []string, expiresAt *time.Time) (raw string, tok *ent.ApiToken, err error) {
	raw, err = randomToken()
	if err != nil {
		return "", nil, err
	}
	if scope == nil {
		scope = []string{}
	}
	tok, err = s.ent.ApiToken.Create().
		SetTokenHash(hashToken(raw)).
		SetName(name).
		SetRunner(runner).
		SetScope(scope).
		SetNillableExpiresAt(expiresAt).
		SetOwnerID(userID).
		Save(ctx)
	if err != nil {
		return "", nil, err
	}
	return raw, tok, nil
}

// RevokeApiToken deletes a bearer token by id. This is how a per-runner credential
// (laptop, home-finance) is independently revoked: the next work-API call carrying
// it resolves to nothing and 401s. Idempotent, a missing id is not an error.
func (s *Service) RevokeApiToken(ctx context.Context, id int) error {
	err := s.ent.ApiToken.DeleteOneID(id).Exec(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	return err
}

// TouchApiToken stamps last_used_at when a token authenticates a work-API call.
// Best-effort by contract: callers log and continue on error rather than fail the
// request the token just authorized.
func (s *Service) TouchApiToken(ctx context.Context, id int) error {
	return s.ent.ApiToken.UpdateOneID(id).SetLastUsedAt(time.Now()).Exec(ctx)
}
