package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/user"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
)

// adminTags groups every Admin Console operation under one OpenAPI tag.
var adminTags = []string{"admin"}

// requireAdmin enforces the admin gate shared by every /api/admin/* operation:
// anonymous is 401, any authenticated non-admin is 403. It returns the
// authenticated admin user on success. This mirrors requireFriend (whatsapp.go)
// but demands the admin tier. The role is read from the User the session
// middleware resolved fresh from the DB, so a just-revoked admin loses access on
// their next request. See ADR 10 (tiered access).
func requireAdmin(ctx context.Context) (*ent.User, error) {
	u := auth.UserFromContext(ctx)
	if u == nil {
		return nil, huma.Error401Unauthorized("authentication required")
	}
	if u.Role != user.RoleAdmin {
		return nil, huma.Error403Forbidden("admin access required")
	}
	return u, nil
}

// registerAdmin wires every Admin Console operation. All of them are admin-gated
// in-handler via requireAdmin and marked with cookieAuthSecurity so /docs and the
// generated client know they need the session cookie.
func (h *Handler) registerAdmin(api huma.API) {
	h.registerAdminGrants(api)
	h.registerAdminProjects(api)
	h.registerAdminUploads(api)
	h.registerAdminPlaylists(api)
	h.registerAdminJobs(api)
}
