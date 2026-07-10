package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
)

// cookieAuthSecurity marks an operation as requiring the session cookie. The
// scheme itself is registered on the OpenAPI components in server.New.
var cookieAuthSecurity = []map[string][]string{{"cookieAuth": {}}}

// bearerAuthSecurity marks an operation as requiring a scope-only bearer token
// (the external-runner work API, ADR 0014). It mirrors cookieAuthSecurity but for
// the bearer scheme: work operations carry this and gate on token scope, never on
// the admin/friend role.
var bearerAuthSecurity = []map[string][]string{{"bearerAuth": {}}}

type userOutput struct {
	Body struct {
		ID                 int     `json:"id"`
		Email              string  `json:"email"`
		Name               string  `json:"name"`
		AvatarURL          string  `json:"avatar_url"`
		Nickname           *string `json:"nickname" doc:"Self-chosen display name; null when unset. Overrides name for display (precedence nickname ?? name ?? email). See ADR 10"`
		Role               string  `json:"role" enum:"admin,friend,member" doc:"Access tier (see ADR 10): admin and friend are allowlist-conferred, everyone else is member"`
		GreetedRole        *string `json:"greeted_role" enum:"admin,friend,member" doc:"The role at which the user was last shown the full Welcome; null means never welcomed. Server-owned (see ADR 10)"`
		DefaultCountryCode string  `json:"default_country_code" doc:"Default WhatsApp country code (digits only) that replaces a leading trunk 0; overridable per recipient list (see ADR 11)"`
	}
}

func toUserOutput(u *ent.User) *userOutput {
	out := &userOutput{}
	out.Body.ID = u.ID
	out.Body.Email = u.Email
	out.Body.Name = u.Name
	out.Body.AvatarURL = u.AvatarURL
	out.Body.Nickname = u.Nickname
	out.Body.Role = string(u.Role)
	if u.GreetedRole != nil {
		gr := string(*u.GreetedRole)
		out.Body.GreetedRole = &gr
	}
	out.Body.DefaultCountryCode = u.DefaultCountryCode
	return out
}

// optionalNullableString is a PATCH body field that captures three request states
// a plain *string cannot tell apart: the key absent (leave the column untouched),
// an explicit JSON null (clear it), and a string value (set it). PATCH semantics
// need the absent/null split so an update that omits the field never clears it.
type optionalNullableString struct {
	Set   bool    // the key was present in the request body
	Value *string // nil when the value was JSON null
}

func (o *optionalNullableString) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	o.Value = &s
	return nil
}

// Schema declares the field as an optional, nullable string so both a string and
// an explicit null clear request validation (Huma's reflected schema would reject
// null otherwise). Implements huma.SchemaProvider.
func (optionalNullableString) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Nullable: true}
}

// updateUserInput is the PATCH /api/auth/me body. Every field is optional; an
// omitted field leaves the corresponding value untouched (see auth.ProfileUpdate).
// The subject is always the User resolved from the session cookie, so there is no
// user id in the body. See ADR 10.
type updateUserInput struct {
	Body struct {
		DefaultCountryCode *string                `json:"default_country_code,omitempty" pattern:"^[0-9]{1,4}$" doc:"Default WhatsApp country code (digits only, no +), 1-4 digits. Omit to leave unchanged."`
		Nickname           optionalNullableString `json:"nickname,omitempty" doc:"Self-chosen display name, 1-40 characters after trimming (control characters are stripped). Send null to clear it; omit to leave it unchanged. See ADR 10."`
		AckWelcome         bool                   `json:"ack_welcome,omitempty" doc:"When true, records that the user has seen the Welcome at their current role (sets greeted_role server-side to the caller's own role; a client cannot assert a role). See ADR 10."`
	}
}

// sanitizeNickname trims a submitted nickname, drops control characters, and
// enforces a 1-40 rune length. It returns the cleaned value and whether it is
// valid. The Ent schema (MaxRuneLen 40) is the backstop; this is the primary,
// user-facing validation. See ADR 10.
func sanitizeNickname(raw string) (string, bool) {
	var b strings.Builder
	for _, r := range raw {
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	cleaned := strings.TrimSpace(b.String())
	if n := utf8.RuneCountInString(cleaned); n < 1 || n > 40 {
		return "", false
	}
	return cleaned, true
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
		Summary:     "Update the current user's profile and settings",
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

		upd := auth.ProfileUpdate{
			CountryCode: in.Body.DefaultCountryCode,
			AckWelcome:  in.Body.AckWelcome,
		}
		if in.Body.Nickname.Set {
			upd.NicknameSet = true
			if in.Body.Nickname.Value != nil {
				clean, ok := sanitizeNickname(*in.Body.Nickname.Value)
				if !ok {
					return nil, huma.Error422UnprocessableEntity("nickname must be 1-40 characters after trimming and contain no control characters")
				}
				upd.Nickname = &clean
			}
			// A present-but-null nickname clears it (NicknameSet true, Nickname nil).
		}

		updated, err := svc.UpdateProfile(ctx, u, upd)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to update profile", err)
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
