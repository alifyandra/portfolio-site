package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
)

// cookieAuthSecurity marks an operation as requiring the session cookie. The
// scheme itself is registered on the OpenAPI components in server.New.
var cookieAuthSecurity = []map[string][]string{{"cookieAuth": {}}}

type userOutput struct {
	Body struct {
		ID                 int    `json:"id"`
		Email              string `json:"email"`
		Name               string `json:"name"`
		AvatarURL          string `json:"avatar_url"`
		Role               string `json:"role" enum:"admin,friend,member" doc:"Access tier (see ADR 10): admin and friend are allowlist-conferred, everyone else is member"`
		DefaultCountryCode string `json:"default_country_code" doc:"Default WhatsApp country code (digits only) that replaces a leading trunk 0; overridable per recipient list (see ADR 11)"`
	}
}

func toUserOutput(u *ent.User) *userOutput {
	out := &userOutput{}
	out.Body.ID = u.ID
	out.Body.Email = u.Email
	out.Body.Name = u.Name
	out.Body.AvatarURL = u.AvatarURL
	out.Body.Role = string(u.Role)
	out.Body.DefaultCountryCode = u.DefaultCountryCode
	return out
}

// updateUserInput is the PATCH /api/auth/me body. Only user-editable settings are
// exposed; the country code is validated as 1-4 digits at the edge and again by the
// Ent schema.
type updateUserInput struct {
	Body struct {
		DefaultCountryCode string `json:"default_country_code" pattern:"^[0-9]{1,4}$" doc:"Default WhatsApp country code (digits only, no +), 1-4 digits"`
	}
}

// messageOutput is a small JSON acknowledgement that may also clear the session
// cookie via Set-Cookie.
type messageOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      struct {
		Message string `json:"message"`
	}
}

// requireAuth returns the auth service, or a 503 when it is not wired in. The
// only build with a nil Auth is cmd/spec, which registers operations to emit the
// spec but never serves requests; the guard keeps these handlers from panicking
// and mirrors the nil-checks in server.New.
func (h *Handler) requireAuth() (*auth.Service, error) {
	if h.deps.Auth == nil {
		return nil, huma.Error503ServiceUnavailable("authentication is not available")
	}
	return h.deps.Auth, nil
}

func (h *Handler) registerAuth(api huma.API) {
	tags := []string{"auth"}

	huma.Register(api, huma.Operation{
		OperationID: "get-current-user",
		Method:      http.MethodGet,
		Path:        "/api/auth/me",
		Summary:     "Get the current authenticated user",
		Tags:        tags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, _ *struct{}) (*userOutput, error) {
		u := auth.UserFromContext(ctx)
		if u == nil {
			return nil, huma.Error401Unauthorized("not authenticated")
		}
		return toUserOutput(u), nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "update-current-user",
		Method:      http.MethodPatch,
		Path:        "/api/auth/me",
		Summary:     "Update the current user's settings",
		Tags:        tags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *updateUserInput) (*userOutput, error) {
		svc, err := h.requireAuth()
		if err != nil {
			return nil, err
		}
		u := auth.UserFromContext(ctx)
		if u == nil {
			return nil, huma.Error401Unauthorized("not authenticated")
		}
		updated, err := svc.UpdateDefaultCountryCode(ctx, u.ID, in.Body.DefaultCountryCode)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to update settings", err)
		}
		return toUserOutput(updated), nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "logout",
		Method:        http.MethodPost,
		Path:          "/api/auth/logout",
		Summary:       "Log out the current device",
		Tags:          tags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusOK,
	}, func(ctx context.Context, _ *struct{}) (*messageOutput, error) {
		svc, err := h.requireAuth()
		if err != nil {
			return nil, err
		}
		// The raw token is stashed on the context by the auth middleware; the
		// HttpOnly cookie is never an operation input.
		if err := svc.RevokeSession(ctx, auth.TokenFromContext(ctx)); err != nil {
			return nil, huma.Error500InternalServerError("logout failed", err)
		}
		out := &messageOutput{SetCookie: svc.ClearSessionCookie()}
		out.Body.Message = "Logged out."
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "logout-all",
		Method:        http.MethodPost,
		Path:          "/api/auth/logout-all",
		Summary:       "Log out every device for the current user",
		Tags:          tags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusOK,
	}, func(ctx context.Context, _ *struct{}) (*messageOutput, error) {
		svc, err := h.requireAuth()
		if err != nil {
			return nil, err
		}
		u := auth.UserFromContext(ctx)
		if u == nil {
			return nil, huma.Error401Unauthorized("not authenticated")
		}
		if err := svc.RevokeAllForUser(ctx, u.ID); err != nil {
			return nil, huma.Error500InternalServerError("logout failed", err)
		}
		out := &messageOutput{SetCookie: svc.ClearSessionCookie()}
		out.Body.Message = "Logged out everywhere."
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "delete-account",
		Method:        http.MethodDelete,
		Path:          "/api/auth/me",
		Summary:       "Permanently delete the current user's account",
		Tags:          tags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusOK,
	}, func(ctx context.Context, _ *struct{}) (*messageOutput, error) {
		svc, err := h.requireAuth()
		if err != nil {
			return nil, err
		}
		u := auth.UserFromContext(ctx)
		if u == nil {
			return nil, huma.Error401Unauthorized("not authenticated")
		}
		sole, err := svc.IsSoleAdmin(ctx, u)
		if err != nil {
			return nil, huma.Error500InternalServerError("could not delete account", err)
		}
		if sole {
			return nil, huma.Error409Conflict("the sole admin cannot delete their own account")
		}
		if err := svc.DeleteUser(ctx, u.ID); err != nil {
			return nil, huma.Error500InternalServerError("could not delete account", err)
		}
		out := &messageOutput{SetCookie: svc.ClearSessionCookie()}
		out.Body.Message = "Account deleted."
		return out, nil
	})
}
