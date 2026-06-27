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
		ID        int    `json:"id"`
		Email     string `json:"email"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
		Role      string `json:"role"`
	}
}

func toUserOutput(u *ent.User) *userOutput {
	out := &userOutput{}
	out.Body.ID = u.ID
	out.Body.Email = u.Email
	out.Body.Name = u.Name
	out.Body.AvatarURL = u.AvatarURL
	out.Body.Role = string(u.Role)
	return out
}

// sessionCookieInput reads the raw session token from the request cookie.
type sessionCookieInput struct {
	Session string `cookie:"session"`
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
		OperationID:   "logout",
		Method:        http.MethodPost,
		Path:          "/api/auth/logout",
		Summary:       "Log out the current device",
		Tags:          tags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusOK,
	}, func(ctx context.Context, in *sessionCookieInput) (*messageOutput, error) {
		svc, err := h.requireAuth()
		if err != nil {
			return nil, err
		}
		if err := svc.RevokeSession(ctx, in.Session); err != nil {
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
