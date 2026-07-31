package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/wishlistitem"
)

// The wishlist write side (portfolio-site#123): admin-only create/update/delete for the
// one-off wants the read side (GET /api/finance/wishlist) and the list_wishlist MCP tool
// serve. There is no admin list endpoint on purpose: the read endpoint with status=all
// already returns every row, and duplicating it would risk the two drifting.
//
// The lifecycle is enforced here, not in the UI: moving status out of wanted stamps
// resolved_at and moving it back clears it, so a client cannot forget. Rows are only
// hard-deleted for mistakes; a bought or abandoned item stays for history.

// AdminWishlistItemDTO is the management-facing shape of a WishlistItem: everything the
// read DTO carries plus the timestamps the console shows.
type AdminWishlistItemDTO struct {
	ID               int      `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Amount           *float64 `json:"amount" doc:"Expected cost; null when the price is unknown (NOT free)"`
	AmountIsEstimate bool     `json:"amount_is_estimate"`
	Currency         string   `json:"currency"`
	Priority         string   `json:"priority" enum:"low,medium,high"`
	Status           string   `json:"status" enum:"wanted,bought,abandoned"`
	Deadline         *string  `json:"deadline" doc:"Soft want-it-by date (YYYY-MM-DD); null when there is no date"`
	ResolvedAt       *string  `json:"resolved_at" doc:"RFC3339 time the item left wanted; null while still wanted"`
	Link             string   `json:"link"`
	ImageKey         string   `json:"image_key"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
}

func toAdminWishlistItemDTO(w *ent.WishlistItem) AdminWishlistItemDTO {
	return AdminWishlistItemDTO{
		ID:               w.ID,
		Name:             w.Name,
		Description:      w.Description,
		Amount:           w.Amount,
		AmountIsEstimate: w.AmountIsEstimate,
		Currency:         w.Currency,
		Priority:         string(w.Priority),
		Status:           string(w.Status),
		Deadline:         dateOnlyPtr(w.Deadline),
		ResolvedAt:       rfc3339Ptr(w.ResolvedAt),
		Link:             w.Link,
		ImageKey:         w.ImageKey,
		CreatedAt:        w.CreatedAt.UTC().Format(http.TimeFormat),
		UpdatedAt:        w.UpdatedAt.UTC().Format(http.TimeFormat),
	}
}

type adminWishlistItemOutput struct {
	Body AdminWishlistItemDTO
}

type createWishlistItemInput struct {
	Body struct {
		Name             string   `json:"name" minLength:"1" maxLength:"200" doc:"What the thing is, short enough to scan in a list"`
		Description      string   `json:"description,omitempty" doc:"Longer note: why it is wanted, which model, what was ruled out"`
		Amount           *float64 `json:"amount,omitempty" doc:"Expected cost; omit when the price is unknown (which is NOT free)"`
		AmountIsEstimate *bool    `json:"amount_is_estimate,omitempty" doc:"Defaults to true (the amount is a guess, not a quote)"`
		Currency         string   `json:"currency,omitempty" doc:"ISO currency code; defaults to AUD"`
		Priority         string   `json:"priority,omitempty" enum:"low,medium,high" doc:"Defaults to medium"`
		Status           string   `json:"status,omitempty" enum:"wanted,bought,abandoned" doc:"Defaults to wanted; creating straight into bought/abandoned stamps resolved_at"`
		Deadline         string   `json:"deadline,omitempty" doc:"Soft want-it-by date, YYYY-MM-DD; omit for no date"`
		Link             string   `json:"link,omitempty" doc:"Product/reference URL; never fetched by the backend"`
		ImageKey         string   `json:"image_key,omitempty" doc:"S3 object key returned by the upload presign endpoint (kind=wishlist)"`
	}
}

// updateWishlistItemInput is a PATCH: every field is a pointer so an omitted field
// leaves the column untouched. resolved_at is deliberately absent, because the server
// derives it from a status move. deadline takes an empty string to clear it, and
// amount_unknown moves a priced item back to "price unknown" (a bare null amount is
// indistinguishable from an omitted one over JSON).
type updateWishlistItemInput struct {
	ID   int `path:"id" doc:"Wishlist item ID"`
	Body struct {
		Name             *string  `json:"name,omitempty" minLength:"1" maxLength:"200"`
		Description      *string  `json:"description,omitempty"`
		Amount           *float64 `json:"amount,omitempty" doc:"Expected cost; ignored when amount_unknown is true"`
		AmountUnknown    *bool    `json:"amount_unknown,omitempty" doc:"Set true to move the price back to unknown; takes precedence over amount"`
		AmountIsEstimate *bool    `json:"amount_is_estimate,omitempty"`
		Currency         *string  `json:"currency,omitempty"`
		Priority         *string  `json:"priority,omitempty" enum:"low,medium,high"`
		Status           *string  `json:"status,omitempty" enum:"wanted,bought,abandoned" doc:"Moving out of wanted stamps resolved_at; moving back to wanted clears it"`
		Deadline         *string  `json:"deadline,omitempty" doc:"YYYY-MM-DD; send an empty string to clear the deadline"`
		Link             *string  `json:"link,omitempty"`
		ImageKey         *string  `json:"image_key,omitempty" doc:"S3 object key; send an empty string to drop the image"`
	}
}

type wishlistItemIDInput struct {
	ID int `path:"id" doc:"Wishlist item ID"`
}

func (h *Handler) registerAdminWishlist(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID:   "create-wishlist-item",
		Method:        http.MethodPost,
		Path:          "/api/admin/wishlist",
		Summary:       "Create a wishlist item",
		Tags:          adminTags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createWishlistItemInput) (*adminWishlistItemOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		// parseQueryDate is the shared YYYY-MM-DD reader: empty means no date, malformed
		// is a 422. A parsed date is already UTC midnight.
		deadline, err := parseQueryDate(in.Body.Deadline)
		if err != nil {
			return nil, err
		}
		create := h.deps.Ent.WishlistItem.Create().
			SetName(strings.TrimSpace(in.Body.Name)).
			SetDescription(in.Body.Description).
			SetLink(in.Body.Link).
			SetImageKey(in.Body.ImageKey).
			SetNillableAmount(in.Body.Amount).
			SetNillableDeadline(deadline)
		if in.Body.AmountIsEstimate != nil {
			create.SetAmountIsEstimate(*in.Body.AmountIsEstimate)
		}
		if c := strings.TrimSpace(in.Body.Currency); c != "" {
			create.SetCurrency(c)
		}
		if in.Body.Priority != "" {
			create.SetPriority(wishlistitem.Priority(in.Body.Priority))
		}
		if in.Body.Status != "" {
			status := wishlistitem.Status(in.Body.Status)
			create.SetStatus(status)
			// Same rule as PATCH: anything that is not wanted carries the moment it was
			// decided, so an item logged as already bought is not left with a null.
			if status != wishlistitem.StatusWanted {
				create.SetResolvedAt(time.Now().UTC())
			}
		}
		w, err := create.Save(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to create wishlist item", err)
		}
		return &adminWishlistItemOutput{Body: toAdminWishlistItemDTO(w)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "update-wishlist-item",
		Method:      http.MethodPatch,
		Path:        "/api/admin/wishlist/{id}",
		Summary:     "Update a wishlist item (partial; status moves stamp resolved_at)",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *updateWishlistItemInput) (*adminWishlistItemOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		// The current row decides whether this PATCH is an actual status MOVE, so a
		// repeated "set bought" does not keep re-stamping resolved_at.
		cur, err := h.deps.Ent.WishlistItem.Get(ctx, in.ID)
		if ent.IsNotFound(err) {
			return nil, huma.Error404NotFound("wishlist item not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load wishlist item", err)
		}

		upd := h.deps.Ent.WishlistItem.UpdateOneID(in.ID)
		if in.Body.Name != nil {
			upd.SetName(strings.TrimSpace(*in.Body.Name))
		}
		if in.Body.Description != nil {
			upd.SetDescription(*in.Body.Description)
		}
		switch {
		case in.Body.AmountUnknown != nil && *in.Body.AmountUnknown:
			upd.ClearAmount()
		case in.Body.Amount != nil:
			upd.SetAmount(*in.Body.Amount)
		}
		if in.Body.AmountIsEstimate != nil {
			upd.SetAmountIsEstimate(*in.Body.AmountIsEstimate)
		}
		if in.Body.Currency != nil {
			upd.SetCurrency(strings.TrimSpace(*in.Body.Currency))
		}
		if in.Body.Priority != nil {
			upd.SetPriority(wishlistitem.Priority(*in.Body.Priority))
		}
		if in.Body.Deadline != nil {
			if strings.TrimSpace(*in.Body.Deadline) == "" {
				upd.ClearDeadline()
			} else {
				deadline, err := parseQueryDate(*in.Body.Deadline)
				if err != nil {
					return nil, err
				}
				upd.SetNillableDeadline(deadline)
			}
		}
		if in.Body.Link != nil {
			upd.SetLink(*in.Body.Link)
		}
		if in.Body.ImageKey != nil {
			upd.SetImageKey(*in.Body.ImageKey)
		}
		if in.Body.Status != nil {
			status := wishlistitem.Status(*in.Body.Status)
			upd.SetStatus(status)
			if status != cur.Status {
				if status == wishlistitem.StatusWanted {
					upd.ClearResolvedAt()
				} else {
					upd.SetResolvedAt(time.Now().UTC())
				}
			}
		}
		w, err := upd.Save(ctx)
		if ent.IsNotFound(err) {
			return nil, huma.Error404NotFound("wishlist item not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to update wishlist item", err)
		}
		return &adminWishlistItemOutput{Body: toAdminWishlistItemDTO(w)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "delete-wishlist-item",
		Method:        http.MethodDelete,
		Path:          "/api/admin/wishlist/{id}",
		Summary:       "Delete a wishlist item (for mistakes; resolved items stay for history)",
		Tags:          adminTags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *wishlistItemIDInput) (*struct{}, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		err := h.deps.Ent.WishlistItem.DeleteOneID(in.ID).Exec(ctx)
		if ent.IsNotFound(err) {
			return nil, huma.Error404NotFound("wishlist item not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to delete wishlist item", err)
		}
		return &struct{}{}, nil
	})
}
