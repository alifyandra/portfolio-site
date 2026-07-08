package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/accessgrant"
)

// AccessGrantDTO is the frontend-facing shape of an AccessGrant.
type AccessGrantDTO struct {
	Email     string `json:"email"`
	Tier      string `json:"tier"`
	CreatedAt string `json:"created_at"`
}

func toAccessGrantDTO(g *ent.AccessGrant) AccessGrantDTO {
	return AccessGrantDTO{
		Email:     g.Email,
		Tier:      string(g.Tier),
		CreatedAt: g.CreatedAt.UTC().Format(http.TimeFormat),
	}
}

type listGrantsOutput struct {
	Body struct {
		Grants []AccessGrantDTO `json:"grants"`
	}
}

type createGrantInput struct {
	Body struct {
		Email string `json:"email" format:"email" minLength:"3" maxLength:"254" doc:"Email to grant access to; normalized to lowercase before storage"`
		Tier  string `json:"tier" enum:"admin,friend" doc:"Access tier to confer (admin or friend)"`
	}
}

type grantOutput struct {
	Body AccessGrantDTO
}

type deleteGrantInput struct {
	Email string `path:"email" doc:"Email of the grant to revoke (case-insensitive; matched after normalization)"`
}

func (h *Handler) registerAdminGrants(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-access-grants",
		Method:      http.MethodGet,
		Path:        "/api/admin/access-grants",
		Summary:     "List all access grants",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, _ *struct{}) (*listGrantsOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		rows, err := h.deps.Ent.AccessGrant.Query().
			Order(ent.Asc(accessgrant.FieldEmail)).
			All(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to list access grants", err)
		}
		out := &listGrantsOutput{}
		out.Body.Grants = make([]AccessGrantDTO, 0, len(rows))
		for _, g := range rows {
			out.Body.Grants = append(out.Body.Grants, toAccessGrantDTO(g))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "create-access-grant",
		Method:      http.MethodPost,
		Path:        "/api/admin/access-grants",
		Summary:     "Grant (or update) an access tier for an email",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *createGrantInput) (*grantOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		email := strings.ToLower(strings.TrimSpace(in.Body.Email))
		if email == "" {
			return nil, huma.Error422UnprocessableEntity("email is required")
		}
		// Huma validates tier against the enum at the edge; re-check as a backstop.
		if in.Body.Tier != string(accessgrant.TierAdmin) && in.Body.Tier != string(accessgrant.TierFriend) {
			return nil, huma.Error422UnprocessableEntity("tier must be admin or friend")
		}
		tier := accessgrant.Tier(in.Body.Tier)

		// Idempotent upsert on email: update the tier if a grant exists, else create.
		var g *ent.AccessGrant
		existing, err := h.deps.Ent.AccessGrant.Query().Where(accessgrant.EmailEQ(email)).Only(ctx)
		switch {
		case err == nil:
			g, err = existing.Update().SetTier(tier).Save(ctx)
			if err != nil {
				return nil, huma.Error500InternalServerError("failed to update access grant", err)
			}
		case ent.IsNotFound(err):
			g, err = h.deps.Ent.AccessGrant.Create().SetEmail(email).SetTier(tier).Save(ctx)
			if err != nil {
				return nil, huma.Error500InternalServerError("failed to create access grant", err)
			}
		default:
			return nil, huma.Error500InternalServerError("failed to load access grant", err)
		}

		// Eagerly recompute the affected user's role so the grant applies before
		// their next login (see auth.RecomputeUserRole).
		if h.deps.Auth != nil {
			if err := h.deps.Auth.RecomputeUserRole(ctx, email); err != nil {
				return nil, huma.Error500InternalServerError("failed to apply grant to user", err)
			}
		}
		return &grantOutput{Body: toAccessGrantDTO(g)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "delete-access-grant",
		Method:        http.MethodDelete,
		Path:          "/api/admin/access-grants/{email}",
		Summary:       "Revoke an access grant by email",
		Tags:          adminTags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *deleteGrantInput) (*struct{}, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		email := strings.ToLower(strings.TrimSpace(in.Email))
		n, err := h.deps.Ent.AccessGrant.Delete().Where(accessgrant.EmailEQ(email)).Exec(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to revoke access grant", err)
		}
		if n == 0 {
			return nil, huma.Error404NotFound("access grant not found")
		}
		// Eagerly recompute the affected user's role down to the env-derived tier.
		if h.deps.Auth != nil {
			if err := h.deps.Auth.RecomputeUserRole(ctx, email); err != nil {
				return nil, huma.Error500InternalServerError("failed to apply revocation to user", err)
			}
		}
		return &struct{}{}, nil
	})
}
