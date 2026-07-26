package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/oauthauthcode"
	"github.com/alifyandra/portfolio-site/backend/ent/oauthtoken"
)

// tokenBytes is the entropy (in bytes) of every opaque code/token this server
// mints. 32 bytes = 256 bits, the same as the session and API-token generators.
const tokenBytes = 32

// errBadGrant is the internal sentinel for any authorization-code or refresh-token
// failure the token endpoint maps to the OAuth "invalid_grant" error. It is
// deliberately opaque: the client is never told which specific check failed.
var errBadGrant = errors.New("oauth: invalid grant")

// randomToken returns a URL-safe random string with tokenBytes of entropy.
func randomToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken is the at-rest representation of a code/token: only the SHA-256 hash
// is stored, so a database leak yields nothing usable. Mirrors auth.hashToken.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// verifyPKCE checks a PKCE S256 challenge: base64url(sha256(verifier)) must equal
// the stored challenge. The comparison is constant time. An empty verifier or
// challenge never verifies (a missing challenge was already rejected at authorize).
func verifyPKCE(verifier, challenge string) bool {
	if verifier == "" || challenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}

// codeGrant is the state a freshly minted authorization code binds.
type codeGrant struct {
	userID        int
	clientID      string
	redirectURI   string
	codeChallenge string
	resource      string
	scope         string
}

// mintAuthCode creates a one-time PKCE authorization code and returns the RAW code
// (only its hash is stored). The code expires in authCodeTTL.
func (s *Service) mintAuthCode(ctx context.Context, g codeGrant) (string, error) {
	raw, err := randomToken()
	if err != nil {
		return "", err
	}
	_, err = s.ent.OAuthAuthCode.Create().
		SetCodeHash(hashToken(raw)).
		SetClientID(g.clientID).
		SetRedirectURI(g.redirectURI).
		SetCodeChallenge(g.codeChallenge).
		SetResource(g.resource).
		SetScope(g.scope).
		SetExpiresAt(s.now().Add(authCodeTTL)).
		SetOwnerID(g.userID).
		Save(ctx)
	if err != nil {
		return "", err
	}
	return raw, nil
}

// tokenPair is what the token endpoint returns to the client.
type tokenPair struct {
	AccessToken  string
	RefreshToken string // empty when offline_access was not granted
	ExpiresIn    int
	Scope        string
}

// redeemAuthCode exchanges a raw authorization code for a token pair. It verifies
// the PKCE code_verifier, the redirect_uri, the client_id and the resource all
// match what the code was bound to, then atomically consumes the code (one-time)
// and issues the tokens with the audience stamped to the code's resource. Any
// mismatch, or a used/expired/unknown code, returns errBadGrant. A verification
// failure does NOT consume the code, so a client that fat-fingers the verifier can
// retry within the 60s window; only a successful redemption burns it.
func (s *Service) redeemAuthCode(ctx context.Context, rawCode, verifier, redirectURI, clientID, resource string) (tokenPair, error) {
	code, err := s.ent.OAuthAuthCode.Query().
		Where(oauthauthcode.CodeHash(hashToken(rawCode))).
		WithOwner().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return tokenPair{}, errBadGrant
		}
		return tokenPair{}, err
	}
	if code.UsedAt != nil || !code.ExpiresAt.After(s.now()) {
		return tokenPair{}, errBadGrant
	}
	// The audience is bound to code.Resource (validated == the MCP URL at authorize),
	// which is what stamps the token audience below — so a client that echoes resource
	// at the token step must match it, but one that omits it is not hard-failed (the
	// binding is already carried by the code). client_id, redirect_uri and PKCE must
	// all match exactly.
	if code.ClientID != clientID ||
		code.RedirectURI != redirectURI ||
		(resource != "" && code.Resource != resource) ||
		!verifyPKCE(verifier, code.CodeChallenge) {
		return tokenPair{}, errBadGrant
	}
	if code.Edges.Owner == nil { // orphaned code (owner deleted)
		return tokenPair{}, errBadGrant
	}

	// Atomically claim the code: the conditional update on used_at IS NULL is the
	// one-time guard under concurrency (two exchanges of the same code race here;
	// exactly one wins). A zero-row result means someone already consumed it.
	n, err := s.ent.OAuthAuthCode.Update().
		Where(oauthauthcode.ID(code.ID), oauthauthcode.UsedAtIsNil()).
		SetUsedAt(s.now()).
		Save(ctx)
	if err != nil {
		return tokenPair{}, err
	}
	if n != 1 {
		return tokenPair{}, errBadGrant
	}

	// A brand-new rotation lineage starts here: every token descended from this
	// grant shares this family_id, so a later refresh-reuse can revoke them all.
	familyID, err := randomToken()
	if err != nil {
		return tokenPair{}, err
	}
	return s.issueTokens(ctx, code.Edges.Owner.ID, clientID, code.Resource, code.Scope, familyID)
}

// issueTokens mints an OAuthToken row (access + optional refresh) for a user. A
// refresh token is only issued when offline_access is among the granted scopes.
// The audience is the resource string, so VerifyMCPAccess can bind on it. familyID
// is the rotation-lineage id: it is freshly minted at the authorization_code grant
// and carried unchanged through every rotation, so a whole lineage can be revoked
// at once when a refresh token is replayed (RFC 9700 §4.14.2).
func (s *Service) issueTokens(ctx context.Context, userID int, clientID, resource, scope, familyID string) (tokenPair, error) {
	access, err := randomToken()
	if err != nil {
		return tokenPair{}, err
	}
	now := s.now()
	create := s.ent.OAuthToken.Create().
		SetAccessTokenHash(hashToken(access)).
		SetFamilyID(familyID).
		SetClientID(clientID).
		SetResource(resource).
		SetScope(scope).
		SetAccessExpiresAt(now.Add(accessTokenTTL)).
		SetOwnerID(userID)

	pair := tokenPair{AccessToken: access, ExpiresIn: int(accessTokenTTL.Seconds()), Scope: scope}

	if scopeContains(scope, offlineAccessScope) {
		refresh, err := randomToken()
		if err != nil {
			return tokenPair{}, err
		}
		create.SetRefreshTokenHash(hashToken(refresh)).
			SetRefreshExpiresAt(now.Add(refreshTokenTTL))
		pair.RefreshToken = refresh
	}

	if _, err := create.Save(ctx); err != nil {
		return tokenPair{}, err
	}
	return pair, nil
}

// rotateRefresh validates a raw refresh token and rotates it: the presented token's
// row is marked rotated (its refresh is now dead) and a brand-new token row (new
// access + new refresh) is issued with the same owner/scope/resource. A rotated,
// revoked, expired or unknown refresh token returns errBadGrant. The rotation is
// atomic on rotated_at IS NULL, so a replayed refresh token cannot mint twice.
func (s *Service) rotateRefresh(ctx context.Context, rawRefresh, clientID string) (tokenPair, error) {
	row, err := s.ent.OAuthToken.Query().
		Where(oauthtoken.RefreshTokenHash(hashToken(rawRefresh))).
		WithOwner().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return tokenPair{}, errBadGrant
		}
		return tokenPair{}, err
	}
	// Refresh-token reuse detection (RFC 9700 §4.14.2): an ALREADY-rotated row means
	// this refresh token was superseded by a legitimate rotation and is now being
	// replayed — the signature of a captured/leaked token. Revoke the ENTIRE family
	// (this row and its live successor chain) before refusing, so the attacker's and
	// the victim's tokens all die and the victim must re-authorize. This is scoped to
	// the already-rotated signal ONLY: an ordinary invalid/expired/wrong-client
	// refusal below must never nuke a healthy family.
	if row.RotatedAt != nil {
		now := s.now()
		_ = s.ent.OAuthToken.Update().
			Where(oauthtoken.FamilyIDEQ(row.FamilyID), oauthtoken.RevokedAtIsNil()).
			SetRevokedAt(now).
			Exec(ctx)
		return tokenPair{}, errBadGrant
	}
	if row.RevokedAt != nil ||
		row.RefreshExpiresAt == nil || !row.RefreshExpiresAt.After(s.now()) ||
		row.ClientID != clientID {
		return tokenPair{}, errBadGrant
	}
	if row.Edges.Owner == nil { // orphaned token (owner deleted)
		return tokenPair{}, errBadGrant
	}

	// Atomically retire the presented refresh token. Losing this race (row already
	// rotated) means the same token was double-submitted concurrently; exactly one
	// caller advances the chain and the loser is refused (no family revocation — the
	// winner's successor is the healthy live chain).
	n, err := s.ent.OAuthToken.Update().
		Where(oauthtoken.ID(row.ID), oauthtoken.RotatedAtIsNil()).
		SetRotatedAt(s.now()).
		Save(ctx)
	if err != nil {
		return tokenPair{}, err
	}
	if n != 1 {
		return tokenPair{}, errBadGrant
	}

	// Carry the same family_id into the successor so the whole lineage stays revocable.
	return s.issueTokens(ctx, row.Edges.Owner.ID, clientID, row.Resource, row.Scope, row.FamilyID)
}

// VerifyMCPAccess is the resource-server check the /mcp handler calls for an OAuth
// bearer (the static finance.read ApiToken path is tried first, separately). It
// returns true only for a live OAuth access token whose audience is THIS resource
// (the MCP URL) and whose scope includes finance.read, and which is neither expired
// nor revoked. Audience binding is enforced here: a token minted for any other
// resource is rejected, so there is no token pass-through.
func (s *Service) VerifyMCPAccess(ctx context.Context, rawToken string) bool {
	if !s.enabled || s.ent == nil || rawToken == "" {
		return false
	}
	row, err := s.ent.OAuthToken.Query().
		Where(oauthtoken.AccessTokenHash(hashToken(rawToken))).
		Only(ctx)
	if err != nil {
		return false
	}
	if row.RevokedAt != nil || !row.AccessExpiresAt.After(s.now()) {
		return false
	}
	if row.Resource != s.resource { // audience binding
		return false
	}
	return scopeContains(row.Scope, financeReadScope)
}

// grantedScope narrows a client's requested scope to what this server supports and
// always includes finance.read (the resource requires it). Unknown scopes are
// dropped. offline_access is kept only if requested.
func grantedScope(requested string) string {
	want := map[string]bool{financeReadScope: true}
	for _, sc := range strings.Fields(requested) {
		if sc == offlineAccessScope {
			want[offlineAccessScope] = true
		}
	}
	out := []string{financeReadScope}
	if want[offlineAccessScope] {
		out = append(out, offlineAccessScope)
	}
	return strings.Join(out, " ")
}

// scopeContains reports whether a space-delimited scope string includes want.
func scopeContains(scope, want string) bool {
	for _, sc := range strings.Fields(scope) {
		if sc == want {
			return true
		}
	}
	return false
}
