package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite" // pure-Go sqlite driver (no CGO) for in-memory test DBs

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/user"
)

// newTestService builds a Service backed by a fresh in-memory SQLite database
// with the Ent schema migrated. Each test gets its own isolated DB.
func newTestService(t *testing.T, adminEmails ...string) (*Service, *ent.Client) {
	t.Helper()
	return newTestServiceCfg(t, Config{AdminEmails: adminEmails})
}

// newTestServiceCfg is newTestService with a caller-supplied Config (e.g. to set
// FriendEmails). Each test still gets its own isolated in-memory SQLite DB.
func newTestServiceCfg(t *testing.T, cfg Config) (*Service, *ent.Client) {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1) // keep the shared in-memory DB alive on one connection
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, db)))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(client, cfg), client
}

func TestUpsertUser_RolesIdentitiesAndReturningLogin(t *testing.T) {
	ctx := context.Background()
	svc, client := newTestService(t, "boss@x.com")

	u, err := svc.upsertUser(ctx, googleClaims{sub: "sub-1", email: "a@x.com", name: "A", picture: "p"})
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	if u.Role != user.RoleMember {
		t.Errorf("non-allowlisted user role = %v, want member", u.Role)
	}
	if client.Identity.Query().CountX(ctx) != 1 {
		t.Errorf("expected one identity after first login")
	}

	admin, err := svc.upsertUser(ctx, googleClaims{sub: "sub-2", email: "boss@x.com"})
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	if admin.Role != user.RoleAdmin {
		t.Errorf("allowlisted user role = %v, want admin", admin.Role)
	}

	// Returning login for the same sub updates the existing user, does not create one.
	u2, err := svc.upsertUser(ctx, googleClaims{sub: "sub-1", email: "a@x.com", name: "A renamed"})
	if err != nil {
		t.Fatalf("returning login: %v", err)
	}
	if u2.ID != u.ID {
		t.Errorf("returning login created a new user (%d != %d)", u2.ID, u.ID)
	}
	if u2.Name != "A renamed" {
		t.Errorf("profile not refreshed on login: name = %q", u2.Name)
	}
	if client.User.Query().CountX(ctx) != 2 {
		t.Errorf("expected exactly two users, got %d", client.User.Query().CountX(ctx))
	}
}

func TestUpsertUser_FriendRoleAssignedAndReasserted(t *testing.T) {
	ctx := context.Background()
	svc, client := newTestServiceCfg(t, Config{FriendEmails: []string{"nayla@x.com"}})

	u, err := svc.upsertUser(ctx, googleClaims{sub: "sub-n", email: "nayla@x.com", name: "Nayla"})
	if err != nil {
		t.Fatalf("friend login: %v", err)
	}
	if u.Role != user.RoleFriend {
		t.Errorf("friend-allowlisted user role = %v, want friend", u.Role)
	}

	// A stray DB edit demotes the friend; the next login must re-assert the role.
	client.User.UpdateOne(u).SetRole(user.RoleMember).ExecX(ctx)
	u2, err := svc.upsertUser(ctx, googleClaims{sub: "sub-n", email: "nayla@x.com", name: "Nayla"})
	if err != nil {
		t.Fatalf("returning friend login: %v", err)
	}
	if u2.Role != user.RoleFriend {
		t.Errorf("friend role not re-asserted on login: got %v, want friend", u2.Role)
	}
}

func TestUpsertUser_EmailCollisionDoesNotLockOutReturningUser(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	if _, err := svc.upsertUser(ctx, googleClaims{sub: "sub-A", email: "shared@x.com", name: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.upsertUser(ctx, googleClaims{sub: "sub-B", email: "b@x.com", name: "B"}); err != nil {
		t.Fatal(err)
	}

	// B's Google email changes to one already owned by A. Login must still
	// succeed (profile updates) and keep B's existing email rather than failing.
	b2, err := svc.upsertUser(ctx, googleClaims{sub: "sub-B", email: "shared@x.com", name: "B2"})
	if err != nil {
		t.Fatalf("returning login must not fail on email collision: %v", err)
	}
	if b2.Email == "shared@x.com" {
		t.Errorf("colliding email should not have been applied")
	}
	if b2.Name != "B2" {
		t.Errorf("non-colliding profile fields should still update")
	}
}

func TestUpsertUser_NewLoginEmailCollisionReturnsErrEmailInUse(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	if _, err := svc.upsertUser(ctx, googleClaims{sub: "sub-A", email: "shared@x.com"}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.upsertUser(ctx, googleClaims{sub: "sub-NEW", email: "shared@x.com"})
	if !errors.Is(err, ErrEmailInUse) {
		t.Fatalf("new login on a taken email: got %v, want ErrEmailInUse", err)
	}
}

func TestSessionResolveAndRevoke(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	u, _ := svc.upsertUser(ctx, googleClaims{sub: "s", email: "u@x.com"})

	raw, err := svc.createSession(ctx, u, "test-agent")
	if err != nil {
		t.Fatal(err)
	}

	got, bumped, err := svc.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != u.ID {
		t.Fatalf("Authenticate did not resolve the session's user")
	}
	if bumped {
		t.Errorf("a freshly minted session should not be bumped")
	}

	if gu, _, _ := svc.Authenticate(ctx, "not-a-real-token"); gu != nil {
		t.Errorf("unknown token resolved a user")
	}

	if err := svc.RevokeSession(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if gu, _, _ := svc.Authenticate(ctx, raw); gu != nil {
		t.Errorf("revoked session still resolves")
	}
}

func TestAuthenticateSlidesNearExpirySession(t *testing.T) {
	ctx := context.Background()
	svc, client := newTestService(t)
	u, _ := svc.upsertUser(ctx, googleClaims{sub: "s", email: "u@x.com"})
	raw, _ := svc.createSession(ctx, u, "")

	// Force the session well into its window so the next auth slides it.
	sess := client.Session.Query().OnlyX(ctx)
	client.Session.UpdateOne(sess).SetExpiresAt(time.Now().Add(2 * time.Hour)).ExecX(ctx)

	_, bumped, err := svc.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bumped {
		t.Errorf("a session past the bump threshold should be slid forward")
	}
	if !client.Session.Query().OnlyX(ctx).ExpiresAt.After(time.Now().Add(29 * 24 * time.Hour)) {
		t.Errorf("expiry was not slid forward to ~30 days")
	}
}

func TestDeleteUserCascades(t *testing.T) {
	ctx := context.Background()
	svc, client := newTestService(t)
	u, _ := svc.upsertUser(ctx, googleClaims{sub: "s", email: "u@x.com"})
	if _, err := svc.createSession(ctx, u, ""); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if client.User.Query().CountX(ctx) != 0 {
		t.Errorf("user not deleted")
	}
	if client.Identity.Query().CountX(ctx) != 0 {
		t.Errorf("identities not cascaded")
	}
	if client.Session.Query().CountX(ctx) != 0 {
		t.Errorf("sessions not cascaded")
	}
}

func TestIsSoleAdmin(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t, "boss@x.com")
	admin, _ := svc.upsertUser(ctx, googleClaims{sub: "a", email: "boss@x.com"})
	member, _ := svc.upsertUser(ctx, googleClaims{sub: "m", email: "m@x.com"})

	if sole, err := svc.IsSoleAdmin(ctx, admin); err != nil || !sole {
		t.Errorf("sole admin: got (%v, %v), want (true, nil)", sole, err)
	}
	if sole, _ := svc.IsSoleAdmin(ctx, member); sole {
		t.Errorf("a member is never the sole admin")
	}
}
