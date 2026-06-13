package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

type createContactInput struct {
	Body struct {
		Name  string `json:"name" minLength:"1" maxLength:"120"`
		Email string `json:"email" format:"email" maxLength:"254"`
		Body  string `json:"body" minLength:"1"`
	}
}

type createContactOutput struct {
	Body struct {
		ID      int    `json:"id"`
		Message string `json:"message"`
	}
}

func (h *Handler) registerContact(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID:   "create-contact-message",
		Method:        http.MethodPost,
		Path:          "/api/contact",
		Summary:       "Submit a contact message",
		Tags:          []string{"contact"},
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createContactInput) (*createContactOutput, error) {
		msg, err := h.deps.Ent.ContactMessage.Create().
			SetName(in.Body.Name).
			SetEmail(in.Body.Email).
			SetBody(in.Body.Body).
			Save(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to save message", err)
		}

		// Future: enqueue a notification job here once the worker handles it.
		// _ = h.deps.Queue.Enqueue(ctx, queue.Job{Type: "contact.notify", ...})

		out := &createContactOutput{}
		out.Body.ID = msg.ID
		out.Body.Message = "Thanks — your message has been received."
		return out, nil
	})
}
