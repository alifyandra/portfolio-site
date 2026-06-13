package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/internal/queue"
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

		// Best-effort: enqueue an email-notification job. The message is already
		// persisted, so a queue hiccup must not fail the request. Skipped when no
		// queue URL is configured (e.g. local dev without the async profile).
		if h.deps.Queue != nil && h.deps.Queue.Configured() {
			payload, _ := json.Marshal(queue.ContactNotifyPayload{
				ID: msg.ID, Name: msg.Name, Email: msg.Email, Body: msg.Body,
			})
			if err := h.deps.Queue.Enqueue(ctx, queue.Job{
				Type:    queue.TypeContactNotify,
				Payload: payload,
			}); err != nil {
				slog.WarnContext(ctx, "failed to enqueue contact notification", "err", err)
			}
		}

		out := &createContactOutput{}
		out.Body.ID = msg.ID
		out.Body.Message = "Thanks — your message has been received."
		return out, nil
	})
}
