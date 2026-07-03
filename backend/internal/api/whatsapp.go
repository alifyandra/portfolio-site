package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/user"
	"github.com/alifyandra/portfolio-site/backend/ent/warecipient"
	"github.com/alifyandra/portfolio-site/backend/ent/warecipientlist"
	"github.com/alifyandra/portfolio-site/backend/ent/watemplate"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
	"github.com/alifyandra/portfolio-site/backend/internal/whatsapp"
)

// requireFriend enforces the friend-or-admin gate shared by every WhatsApp
// operation: anonymous is 401, a plain member is 403. It returns the authenticated
// user on success. See ADR 10 (tiered access) and ADR 11 (friend-gated tool).
func requireFriend(ctx context.Context) (*ent.User, error) {
	u := auth.UserFromContext(ctx)
	if u == nil {
		return nil, huma.Error401Unauthorized("authentication required")
	}
	if u.Role != user.RoleAdmin && u.Role != user.RoleFriend {
		return nil, huma.Error403Forbidden("this tool is available to friends only")
	}
	return u, nil
}

func (h *Handler) registerWhatsApp(api huma.API) {
	h.registerWhatsAppTemplates(api)
	h.registerWhatsAppLists(api)
	h.registerWhatsAppBatches(api)
}

// --- DTOs ---

// TemplateDTO is the frontend-facing shape of a WaTemplate.
type TemplateDTO struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toTemplateDTO(t *ent.WaTemplate) TemplateDTO {
	return TemplateDTO{
		ID:        t.ID,
		Name:      t.Name,
		Body:      t.Body,
		CreatedAt: t.CreatedAt.UTC().Format(http.TimeFormat),
		UpdatedAt: t.UpdatedAt.UTC().Format(http.TimeFormat),
	}
}

// RecipientDTO is one entry in a list. Phone is canonical (digits only).
type RecipientDTO struct {
	Phone string `json:"phone"`
	Name  string `json:"name"`
}

// ListDTO is the frontend-facing shape of a WaRecipientList. Recipients is
// populated only on the single-list GET; the collection GET carries just the
// count to keep PII off the list view.
type ListDTO struct {
	ID             int            `json:"id"`
	Name           string         `json:"name"`
	RecipientCount int            `json:"recipient_count"`
	Recipients     []RecipientDTO `json:"recipients,omitempty"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
}

// --- owner-scoped lookups (return 404 when missing or owned by someone else) ---

func (h *Handler) ownedTemplate(ctx context.Context, uid, id int) (*ent.WaTemplate, error) {
	t, err := h.deps.Ent.WaTemplate.Query().
		Where(watemplate.ID(id), watemplate.HasOwnerWith(user.ID(uid))).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, huma.Error404NotFound("template not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load template", err)
	}
	return t, nil
}

func (h *Handler) ownedList(ctx context.Context, uid, id int) (*ent.WaRecipientList, error) {
	l, err := h.deps.Ent.WaRecipientList.Query().
		Where(warecipientlist.ID(id), warecipientlist.HasOwnerWith(user.ID(uid))).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, huma.Error404NotFound("recipient list not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load recipient list", err)
	}
	return l, nil
}

// --- Templates ---

type listTemplatesOutput struct {
	Body struct {
		Templates []TemplateDTO `json:"templates"`
	}
}

type templateInput struct {
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"120"`
		Body string `json:"body" minLength:"1"`
	}
}

type templateIDInput struct {
	ID int `path:"id" doc:"Template ID"`
}

type updateTemplateInput struct {
	ID   int `path:"id" doc:"Template ID"`
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"120"`
		Body string `json:"body" minLength:"1"`
	}
}

type templateOutput struct {
	Body TemplateDTO
}

func (h *Handler) registerWhatsAppTemplates(api huma.API) {
	tags := []string{"whatsapp"}

	huma.Register(api, huma.Operation{
		OperationID: "list-wa-templates",
		Method:      http.MethodGet,
		Path:        "/api/wa/templates",
		Summary:     "List the caller's WhatsApp message templates",
		Tags:        tags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, _ *struct{}) (*listTemplatesOutput, error) {
		u, err := requireFriend(ctx)
		if err != nil {
			return nil, err
		}
		rows, err := h.deps.Ent.WaTemplate.Query().
			Where(watemplate.HasOwnerWith(user.ID(u.ID))).
			Order(ent.Desc(watemplate.FieldUpdatedAt)).
			All(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to list templates", err)
		}
		out := &listTemplatesOutput{}
		out.Body.Templates = make([]TemplateDTO, 0, len(rows))
		for _, t := range rows {
			out.Body.Templates = append(out.Body.Templates, toTemplateDTO(t))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "create-wa-template",
		Method:        http.MethodPost,
		Path:          "/api/wa/templates",
		Summary:       "Create a WhatsApp message template",
		Tags:          tags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *templateInput) (*templateOutput, error) {
		u, err := requireFriend(ctx)
		if err != nil {
			return nil, err
		}
		t, err := h.deps.Ent.WaTemplate.Create().
			SetName(in.Body.Name).
			SetBody(in.Body.Body).
			SetOwnerID(u.ID).
			Save(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to create template", err)
		}
		return &templateOutput{Body: toTemplateDTO(t)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-wa-template",
		Method:      http.MethodGet,
		Path:        "/api/wa/templates/{id}",
		Summary:     "Get one of the caller's WhatsApp templates",
		Tags:        tags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *templateIDInput) (*templateOutput, error) {
		u, err := requireFriend(ctx)
		if err != nil {
			return nil, err
		}
		t, err := h.ownedTemplate(ctx, u.ID, in.ID)
		if err != nil {
			return nil, err
		}
		return &templateOutput{Body: toTemplateDTO(t)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "update-wa-template",
		Method:      http.MethodPut,
		Path:        "/api/wa/templates/{id}",
		Summary:     "Update one of the caller's WhatsApp templates",
		Tags:        tags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *updateTemplateInput) (*templateOutput, error) {
		u, err := requireFriend(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := h.ownedTemplate(ctx, u.ID, in.ID); err != nil {
			return nil, err
		}
		t, err := h.deps.Ent.WaTemplate.UpdateOneID(in.ID).
			SetName(in.Body.Name).
			SetBody(in.Body.Body).
			Save(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to update template", err)
		}
		return &templateOutput{Body: toTemplateDTO(t)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "delete-wa-template",
		Method:        http.MethodDelete,
		Path:          "/api/wa/templates/{id}",
		Summary:       "Delete one of the caller's WhatsApp templates",
		Tags:          tags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *templateIDInput) (*struct{}, error) {
		u, err := requireFriend(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := h.ownedTemplate(ctx, u.ID, in.ID); err != nil {
			return nil, err
		}
		// Detach the template from any batch history first (the batch keeps its
		// aggregate counts), then delete it, so the FK does not block the delete.
		tx, err := h.deps.Ent.Tx(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to delete template", err)
		}
		if _, err := tx.WaBatch.Update().
			Where(wabatchHasTemplate(in.ID)).
			ClearTemplate().
			Save(ctx); err != nil {
			_ = tx.Rollback()
			return nil, huma.Error500InternalServerError("failed to delete template", err)
		}
		if err := tx.WaTemplate.DeleteOneID(in.ID).Exec(ctx); err != nil {
			_ = tx.Rollback()
			return nil, huma.Error500InternalServerError("failed to delete template", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, huma.Error500InternalServerError("failed to delete template", err)
		}
		return &struct{}{}, nil
	})
}

// --- Recipient lists ---

type listListsOutput struct {
	Body struct {
		Lists []ListDTO `json:"lists"`
	}
}

type listBodyInput struct {
	Name string `json:"name" minLength:"1" maxLength:"120"`
	// RecipientsText is bulk-paste input: one recipient per line, "phone" or
	// "phone,name" (comma or tab separated). Numbers are normalized to
	// international form; invalid lines are reported and skipped.
	RecipientsText string `json:"recipients_text"`
}

type createListInput struct {
	Body listBodyInput
}

type updateListInput struct {
	ID   int `path:"id" doc:"Recipient list ID"`
	Body listBodyInput
}

type listIDInput struct {
	ID int `path:"id" doc:"Recipient list ID"`
}

// listOutput carries the list plus any per-line parse errors from the paste, so
// the user can fix the offending lines without losing the ones that parsed.
type listOutput struct {
	Body struct {
		List         ListDTO              `json:"list"`
		InvalidLines []whatsapp.LineError `json:"invalid_lines,omitempty"`
	}
}

func (h *Handler) registerWhatsAppLists(api huma.API) {
	tags := []string{"whatsapp"}

	huma.Register(api, huma.Operation{
		OperationID: "list-wa-lists",
		Method:      http.MethodGet,
		Path:        "/api/wa/lists",
		Summary:     "List the caller's WhatsApp recipient lists",
		Tags:        tags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, _ *struct{}) (*listListsOutput, error) {
		u, err := requireFriend(ctx)
		if err != nil {
			return nil, err
		}
		rows, err := h.deps.Ent.WaRecipientList.Query().
			Where(warecipientlist.HasOwnerWith(user.ID(u.ID))).
			Order(ent.Desc(warecipientlist.FieldUpdatedAt)).
			All(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to list recipient lists", err)
		}
		out := &listListsOutput{}
		out.Body.Lists = make([]ListDTO, 0, len(rows))
		for _, l := range rows {
			count, err := l.QueryRecipients().Count(ctx)
			if err != nil {
				return nil, huma.Error500InternalServerError("failed to count recipients", err)
			}
			out.Body.Lists = append(out.Body.Lists, ListDTO{
				ID:             l.ID,
				Name:           l.Name,
				RecipientCount: count,
				CreatedAt:      l.CreatedAt.UTC().Format(http.TimeFormat),
				UpdatedAt:      l.UpdatedAt.UTC().Format(http.TimeFormat),
			})
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-wa-list",
		Method:      http.MethodGet,
		Path:        "/api/wa/lists/{id}",
		Summary:     "Get one recipient list with its recipients",
		Tags:        tags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *listIDInput) (*listOutput, error) {
		u, err := requireFriend(ctx)
		if err != nil {
			return nil, err
		}
		l, err := h.ownedList(ctx, u.ID, in.ID)
		if err != nil {
			return nil, err
		}
		recs, err := l.QueryRecipients().
			Order(ent.Asc(warecipient.FieldID)).
			All(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load recipients", err)
		}
		out := &listOutput{}
		out.Body.List = listWithRecipientsDTO(l, recs)
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "create-wa-list",
		Method:        http.MethodPost,
		Path:          "/api/wa/lists",
		Summary:       "Create a recipient list from bulk-pasted numbers",
		Tags:          tags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createListInput) (*listOutput, error) {
		u, err := requireFriend(ctx)
		if err != nil {
			return nil, err
		}
		parsed, invalid := whatsapp.ParseRecipients(in.Body.RecipientsText)
		if len(parsed) > h.deps.WaMaxBatchRecipients {
			return nil, huma.Error422UnprocessableEntity(tooManyRecipientsMsg(len(parsed), h.deps.WaMaxBatchRecipients))
		}

		tx, err := h.deps.Ent.Tx(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to create recipient list", err)
		}
		l, err := tx.WaRecipientList.Create().
			SetName(in.Body.Name).
			SetOwnerID(u.ID).
			Save(ctx)
		if err != nil {
			_ = tx.Rollback()
			return nil, huma.Error500InternalServerError("failed to create recipient list", err)
		}
		if err := bulkCreateRecipients(ctx, tx, l.ID, parsed); err != nil {
			_ = tx.Rollback()
			return nil, huma.Error500InternalServerError("failed to save recipients", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, huma.Error500InternalServerError("failed to create recipient list", err)
		}

		out := &listOutput{}
		out.Body.List = listWithRecipientsDTO(l, nil)
		out.Body.List.RecipientCount = len(parsed)
		out.Body.List.Recipients = recipientsToDTO(parsed)
		out.Body.InvalidLines = invalid
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "update-wa-list",
		Method:      http.MethodPut,
		Path:        "/api/wa/lists/{id}",
		Summary:     "Replace a recipient list's name and members",
		Tags:        tags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *updateListInput) (*listOutput, error) {
		u, err := requireFriend(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := h.ownedList(ctx, u.ID, in.ID); err != nil {
			return nil, err
		}
		parsed, invalid := whatsapp.ParseRecipients(in.Body.RecipientsText)
		if len(parsed) > h.deps.WaMaxBatchRecipients {
			return nil, huma.Error422UnprocessableEntity(tooManyRecipientsMsg(len(parsed), h.deps.WaMaxBatchRecipients))
		}

		tx, err := h.deps.Ent.Tx(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to update recipient list", err)
		}
		l, err := tx.WaRecipientList.UpdateOneID(in.ID).SetName(in.Body.Name).Save(ctx)
		if err != nil {
			_ = tx.Rollback()
			return nil, huma.Error500InternalServerError("failed to update recipient list", err)
		}
		// PUT replaces the whole membership: clear the old recipients, insert the new.
		if _, err := tx.WaRecipient.Delete().
			Where(warecipient.HasListWith(warecipientlist.ID(in.ID))).
			Exec(ctx); err != nil {
			_ = tx.Rollback()
			return nil, huma.Error500InternalServerError("failed to update recipient list", err)
		}
		if err := bulkCreateRecipients(ctx, tx, in.ID, parsed); err != nil {
			_ = tx.Rollback()
			return nil, huma.Error500InternalServerError("failed to save recipients", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, huma.Error500InternalServerError("failed to update recipient list", err)
		}

		out := &listOutput{}
		out.Body.List = listWithRecipientsDTO(l, nil)
		out.Body.List.RecipientCount = len(parsed)
		out.Body.List.Recipients = recipientsToDTO(parsed)
		out.Body.InvalidLines = invalid
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "delete-wa-list",
		Method:        http.MethodDelete,
		Path:          "/api/wa/lists/{id}",
		Summary:       "Delete one of the caller's recipient lists",
		Tags:          tags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *listIDInput) (*struct{}, error) {
		u, err := requireFriend(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := h.ownedList(ctx, u.ID, in.ID); err != nil {
			return nil, err
		}
		tx, err := h.deps.Ent.Tx(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to delete recipient list", err)
		}
		// Detach batch history, drop members, then the list, so no FK blocks it.
		if _, err := tx.WaBatch.Update().Where(wabatchHasList(in.ID)).ClearList().Save(ctx); err != nil {
			_ = tx.Rollback()
			return nil, huma.Error500InternalServerError("failed to delete recipient list", err)
		}
		if _, err := tx.WaRecipient.Delete().
			Where(warecipient.HasListWith(warecipientlist.ID(in.ID))).
			Exec(ctx); err != nil {
			_ = tx.Rollback()
			return nil, huma.Error500InternalServerError("failed to delete recipient list", err)
		}
		if err := tx.WaRecipientList.DeleteOneID(in.ID).Exec(ctx); err != nil {
			_ = tx.Rollback()
			return nil, huma.Error500InternalServerError("failed to delete recipient list", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, huma.Error500InternalServerError("failed to delete recipient list", err)
		}
		return &struct{}{}, nil
	})
}

// bulkCreateRecipients inserts parsed recipients into a list within a tx. A no-op
// for an empty slice.
func bulkCreateRecipients(ctx context.Context, tx *ent.Tx, listID int, parsed []whatsapp.ParsedRecipient) error {
	if len(parsed) == 0 {
		return nil
	}
	builders := make([]*ent.WaRecipientCreate, 0, len(parsed))
	for _, p := range parsed {
		builders = append(builders, tx.WaRecipient.Create().
			SetPhone(p.Phone).
			SetName(p.Name).
			SetListID(listID))
	}
	return tx.WaRecipient.CreateBulk(builders...).Exec(ctx)
}

func listWithRecipientsDTO(l *ent.WaRecipientList, recs []*ent.WaRecipient) ListDTO {
	dto := ListDTO{
		ID:             l.ID,
		Name:           l.Name,
		RecipientCount: len(recs),
		CreatedAt:      l.CreatedAt.UTC().Format(http.TimeFormat),
		UpdatedAt:      l.UpdatedAt.UTC().Format(http.TimeFormat),
	}
	for _, r := range recs {
		dto.Recipients = append(dto.Recipients, RecipientDTO{Phone: r.Phone, Name: r.Name})
	}
	return dto
}

func recipientsToDTO(parsed []whatsapp.ParsedRecipient) []RecipientDTO {
	out := make([]RecipientDTO, 0, len(parsed))
	for _, p := range parsed {
		out = append(out, RecipientDTO{Phone: p.Phone, Name: p.Name})
	}
	return out
}
