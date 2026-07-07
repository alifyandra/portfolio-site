package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/predicate"
	"github.com/alifyandra/portfolio-site/backend/ent/user"
	"github.com/alifyandra/portfolio-site/backend/ent/wabatch"
	"github.com/alifyandra/portfolio-site/backend/ent/warecipient"
	"github.com/alifyandra/portfolio-site/backend/ent/warecipientlist"
	"github.com/alifyandra/portfolio-site/backend/ent/watemplate"
	"github.com/alifyandra/portfolio-site/backend/internal/whatsapp"
)

// errClientGone marks a relay write that failed because the browser disconnected,
// so the orchestrator stops trying to write and just records the batch as failed.
var errClientGone = errors.New("whatsapp: client disconnected")

// wabatchHasTemplate / wabatchHasList are small predicate helpers shared with the
// CRUD deletes, which detach batch history before removing a template or list.
func wabatchHasTemplate(id int) predicate.WaBatch {
	return wabatch.HasTemplateWith(watemplate.ID(id))
}

func wabatchHasList(id int) predicate.WaBatch {
	return wabatch.HasListWith(warecipientlist.ID(id))
}

func tooManyRecipientsMsg(n, limit int) string {
	return fmt.Sprintf("a list may hold at most %d recipients (got %d)", limit, n)
}

// BatchDTO is the frontend-facing shape of a WaBatch for the history view.
type BatchDTO struct {
	ID           int    `json:"id"`
	Status       string `json:"status"`
	TemplateName string `json:"template_name"`
	ListName     string `json:"list_name"`
	Total        int    `json:"total"`
	Sent         int    `json:"sent"`
	Skipped      int    `json:"skipped"`
	Failed       int    `json:"failed"`
	Error        string `json:"error,omitempty"`
	CreatedAt    string `json:"created_at"`
}

func toBatchDTO(b *ent.WaBatch) BatchDTO {
	dto := BatchDTO{
		ID:        b.ID,
		Status:    string(b.Status),
		Total:     b.Total,
		Sent:      b.Sent,
		Skipped:   b.Skipped,
		Failed:    b.Failed,
		Error:     b.Error,
		CreatedAt: b.CreatedAt.UTC().Format(http.TimeFormat),
	}
	if t := b.Edges.Template; t != nil {
		dto.TemplateName = t.Name
	}
	if l := b.Edges.List; l != nil {
		dto.ListName = l.Name
	}
	return dto
}

type listBatchesOutput struct {
	Body struct {
		Batches []BatchDTO `json:"batches"`
		// DailyRemaining is how many more batches the caller may send in the
		// rolling 24h window, floored at zero. See the cap in ADR 11.
		DailyRemaining int `json:"daily_remaining"`
	}
}

type createBatchInput struct {
	Body struct {
		TemplateID int `json:"template_id"`
		ListID     int `json:"list_id"`
	}
}

func (h *Handler) registerWhatsAppBatches(api huma.API) {
	tags := []string{"whatsapp"}

	huma.Register(api, huma.Operation{
		OperationID: "list-wa-batches",
		Method:      http.MethodGet,
		Path:        "/api/wa/batches",
		Summary:     "List the caller's recent WhatsApp batches",
		Tags:        tags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, _ *struct{}) (*listBatchesOutput, error) {
		u, err := requireFriend(ctx)
		if err != nil {
			return nil, err
		}
		rows, err := h.deps.Ent.WaBatch.Query().
			Where(wabatch.HasOwnerWith(user.ID(u.ID))).
			WithTemplate().
			WithList().
			Order(ent.Desc(wabatch.FieldCreatedAt)).
			Limit(20).
			All(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to list batches", err)
		}
		used, err := h.batchesUsedToday(ctx, u.ID)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to compute quota", err)
		}
		out := &listBatchesOutput{}
		out.Body.Batches = make([]BatchDTO, 0, len(rows))
		for _, b := range rows {
			out.Body.Batches = append(out.Body.Batches, toBatchDTO(b))
		}
		out.Body.DailyRemaining = max(0, h.deps.WaMaxBatchesPerDay-used)
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "create-wa-batch",
		Method:      http.MethodPost,
		Path:        "/api/wa/batches",
		Summary:     "Start a WhatsApp batch send (streams QR + live progress as NDJSON)",
		Tags:        tags,
		Security:    cookieAuthSecurity,
	}, h.createBatchHandler)
}

// createBatchHandler validates and creates the batch, then streams the QR and
// per-recipient progress back as newline-delimited JSON. All rejection paths
// (gate, caps, resolution) happen before streaming begins so they can return a
// real HTTP status; once the stream is open, failures are terminal `error`
// events. See docs/whatsapp-sidecar-contract.md.
func (h *Handler) createBatchHandler(ctx context.Context, in *createBatchInput) (*huma.StreamResponse, error) {
	u, err := requireFriend(ctx)
	if err != nil {
		return nil, err
	}
	if h.deps.WA == nil || !h.deps.WA.Configured() {
		return nil, huma.Error503ServiceUnavailable("WhatsApp sending is not configured")
	}

	tmpl, err := h.ownedTemplate(ctx, u.ID, in.Body.TemplateID)
	if err != nil {
		return nil, err
	}
	list, err := h.ownedList(ctx, u.ID, in.Body.ListID)
	if err != nil {
		return nil, err
	}
	recs, err := list.QueryRecipients().Order(ent.Asc(warecipient.FieldID)).All(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load recipients", err)
	}
	if len(recs) == 0 {
		return nil, huma.Error422UnprocessableEntity("the recipient list is empty")
	}
	if len(recs) > h.deps.WaMaxBatchRecipients {
		return nil, huma.Error422UnprocessableEntity(tooManyRecipientsMsg(len(recs), h.deps.WaMaxBatchRecipients))
	}

	used, err := h.batchesUsedToday(ctx, u.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to check the daily limit", err)
	}
	if used >= h.deps.WaMaxBatchesPerDay {
		return nil, huma.NewError(http.StatusTooManyRequests,
			fmt.Sprintf("daily limit reached: at most %d batches per 24 hours", h.deps.WaMaxBatchesPerDay))
	}

	batch, err := h.deps.Ent.WaBatch.Create().
		SetOwnerID(u.ID).
		SetTemplateID(tmpl.ID).
		SetListID(list.ID).
		SetTotal(len(recs)).
		Save(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to create batch", err)
	}

	sess := whatsapp.SessionRequest{
		BatchID:      batch.ID,
		TemplateBody: tmpl.Body,
		Recipients:   make([]whatsapp.SessionRecipient, 0, len(recs)),
	}
	for _, r := range recs {
		sess.Recipients = append(sess.Recipients, whatsapp.SessionRecipient{Phone: r.Phone, Name: r.Name})
	}

	return &huma.StreamResponse{
		Body: func(hctx huma.Context) {
			h.streamBatch(hctx, batch.ID, sess)
		},
	}, nil
}

// streamBatch drives one batch: it dials the sidecar, relays every NDJSON event
// verbatim to the browser, and mirrors each into the WaBatch row's status and
// aggregate counts. If the stream ends without a terminal `done`/`error`, or the
// sidecar rejects/aborts, the batch is marked failed so nothing sticks in
// `running`.
func (h *Handler) streamBatch(hctx huma.Context, batchID int, sess whatsapp.SessionRequest) {
	reqCtx := hctx.Context()
	hctx.SetHeader("Content-Type", "application/x-ndjson")
	hctx.SetHeader("Cache-Control", "no-cache")
	hctx.SetHeader("X-Content-Type-Options", "nosniff")

	w := hctx.BodyWriter()
	flusher, _ := w.(http.Flusher)
	writeLine := func(b []byte) error {
		if _, err := w.Write(append(b, '\n')); err != nil {
			return errClientGone
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	sawTerminal := false
	sawQR := false

	onEvent := func(raw []byte, ev whatsapp.Event) error {
		// Relay first so the browser sees progress even if a DB write lags. Capture
		// the write error rather than returning on it: a terminal done/error event
		// must still be persisted (on a detached context, since a client disconnect
		// cancels reqCtx) so a disconnect on the final line does not lose the true
		// outcome. Non-terminal bookkeeping is skipped once the client is gone.
		writeErr := writeLine(raw)
		switch ev.Type {
		case whatsapp.EventQR:
			// WhatsApp refreshes the QR every ~20s; only the first moves the batch
			// to linking. Later QR lines are still relayed for the browser to redraw.
			if writeErr == nil && !sawQR {
				sawQR = true
				h.setBatchStatus(reqCtx, batchID, wabatch.StatusLinking)
			}
		case whatsapp.EventReady:
			if writeErr == nil {
				h.setBatchStatus(reqCtx, batchID, wabatch.StatusRunning)
			}
		case whatsapp.EventProgress:
			if writeErr == nil {
				h.incrementBatch(reqCtx, batchID, ev.Status)
			}
		case whatsapp.EventDone:
			sawTerminal = true
			h.completeBatchDetached(reqCtx, batchID, ev.Sent, ev.Skipped, ev.Failed)
		case whatsapp.EventError:
			// The terminal error event carries "message" (a failed progress line uses
			// "error"); fall back to "error" defensively so the reason is never blank.
			sawTerminal = true
			msg := ev.Message
			if msg == "" {
				msg = ev.Error
			}
			h.failBatchDetached(reqCtx, batchID, msg)
		}
		return writeErr
	}

	// Provision a sidecar for this batch. In fargate mode this launches an ECS task
	// (a cold start of up to ~2 minutes), emitting provisioning events so the browser
	// can show progress; in static mode it returns the fixed client instantly.
	handle, err := h.deps.WA.Provision(reqCtx, func(msg string) {
		_ = writeLine(provisioningEventJSON(msg))
	})
	if err != nil {
		// A canceled context means the browser hung up during the cold start: record
		// the outcome on a detached context (the provider already stopped any task it
		// launched) and stop, since there is no one to receive a terminal line.
		if errors.Is(err, context.Canceled) || reqCtx.Err() != nil {
			h.failBatchDetached(reqCtx, batchID, "the connection was closed")
			return
		}
		slog.WarnContext(reqCtx, "wa: provision failed", "batch", batchID, "err", err)
		h.failBatch(reqCtx, batchID, "the sender could not be started")
		_ = writeLine(errorEventJSON("the sender could not be started"))
		return
	}
	// Tear the sidecar down on every exit path (StopTask in fargate; a no-op in
	// static). Detached from reqCtx so the teardown still runs after a client
	// disconnect has canceled reqCtx.
	defer func() {
		detached, cancel := context.WithTimeout(context.WithoutCancel(reqCtx), 30*time.Second)
		defer cancel()
		handle.Close(detached)
	}()

	err = handle.Client.StartSession(reqCtx, sess, onEvent)
	if err == nil {
		if !sawTerminal {
			// Stream closed cleanly but never sent a done/error: treat as failed.
			h.failBatch(reqCtx, batchID, "the send ended unexpectedly")
			_ = writeLine([]byte(`{"type":"error","message":"the send ended unexpectedly"}`))
		}
		return
	}

	// The client hung up: the sidecar was aborted; just record the outcome. Use a
	// detached context because reqCtx is already canceled.
	if errors.Is(err, errClientGone) {
		if !sawTerminal {
			h.failBatchDetached(reqCtx, batchID, "the connection was closed")
		}
		return
	}

	// Sidecar-side failure (busy, dial error, transport error). Surface it as a
	// terminal error event and mark the batch failed.
	msg := "the send failed"
	if errors.Is(err, whatsapp.ErrSessionBusy) {
		msg = "a send is already running; wait for it to finish"
	}
	slog.WarnContext(reqCtx, "wa: batch session failed", "batch", batchID, "err", err)
	if !sawTerminal {
		h.failBatch(reqCtx, batchID, msg)
		_ = writeLine(errorEventJSON(msg))
	}
}

// setBatchStatus updates only the status; best-effort (a lost bookkeeping write
// must not abort an in-flight send).
func (h *Handler) setBatchStatus(ctx context.Context, batchID int, status wabatch.Status) {
	if _, err := h.deps.Ent.WaBatch.UpdateOneID(batchID).SetStatus(status).Save(ctx); err != nil {
		slog.WarnContext(ctx, "wa: failed to set batch status", "batch", batchID, "status", status, "err", err)
	}
}

// incrementBatch bumps the aggregate count matching a progress event's status.
func (h *Handler) incrementBatch(ctx context.Context, batchID int, status string) {
	upd := h.deps.Ent.WaBatch.UpdateOneID(batchID)
	switch status {
	case whatsapp.StatusSent:
		upd.AddSent(1)
	case whatsapp.StatusSkipped:
		upd.AddSkipped(1)
	case whatsapp.StatusFailed:
		upd.AddFailed(1)
	default:
		return
	}
	if _, err := upd.Save(ctx); err != nil {
		slog.WarnContext(ctx, "wa: failed to increment batch count", "batch", batchID, "status", status, "err", err)
	}
}

func (h *Handler) failBatch(ctx context.Context, batchID int, msg string) {
	if _, err := h.deps.Ent.WaBatch.UpdateOneID(batchID).
		SetStatus(wabatch.StatusFailed).
		SetError(msg).
		Save(ctx); err != nil {
		slog.WarnContext(ctx, "wa: failed to mark batch failed", "batch", batchID, "err", err)
	}
}

// failBatchDetached marks a batch failed using a context detached from the
// (already-canceled) request context, so the write still lands after the client
// disconnects.
func (h *Handler) failBatchDetached(ctx context.Context, batchID int, msg string) {
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	h.failBatch(detached, batchID, msg)
}

func (h *Handler) completeBatch(ctx context.Context, batchID, sent, skipped, failed int) {
	if _, err := h.deps.Ent.WaBatch.UpdateOneID(batchID).
		SetStatus(wabatch.StatusCompleted).
		SetSent(sent).
		SetSkipped(skipped).
		SetFailed(failed).
		Save(ctx); err != nil {
		slog.WarnContext(ctx, "wa: failed to finalize batch", "batch", batchID, "err", err)
	}
}

// completeBatchDetached finalizes a batch on a context detached from the
// (possibly-canceled) request context, so a terminal done that arrives as the
// client disconnects still records the true outcome instead of being lost.
func (h *Handler) completeBatchDetached(ctx context.Context, batchID, sent, skipped, failed int) {
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	h.completeBatch(detached, batchID, sent, skipped, failed)
}

// batchesUsedToday counts the caller's batches in the rolling 24h window that
// actually did something (sent/skipped/failed > 0), so an aborted QR scan does
// not consume a daily slot. See ADR 11.
func (h *Handler) batchesUsedToday(ctx context.Context, uid int) (int, error) {
	cutoff := time.Now().Add(-24 * time.Hour)
	return h.deps.Ent.WaBatch.Query().
		Where(
			wabatch.HasOwnerWith(user.ID(uid)),
			wabatch.CreatedAtGT(cutoff),
			wabatch.Or(
				wabatch.SentGT(0),
				wabatch.SkippedGT(0),
				wabatch.FailedGT(0),
			),
		).
		Count(ctx)
}

func errorEventJSON(msg string) []byte {
	return eventJSON(whatsapp.EventError, msg)
}

// provisioningEventJSON is the backend-emitted line shown during a fargate cold
// start (never relayed from the sidecar).
func provisioningEventJSON(msg string) []byte {
	return eventJSON(whatsapp.EventProvisioning, msg)
}

// eventJSON builds a one-line {"type","message"} NDJSON event via encoding/json,
// so any control characters in msg are escaped and cannot corrupt the line framing.
func eventJSON(eventType, msg string) []byte {
	b, err := json.Marshal(struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}{Type: eventType, Message: msg})
	if err != nil {
		// Both fields are plain strings, so marshaling cannot actually fail; keep a
		// defensive fallback rather than panic inside a streaming handler.
		return []byte(`{"type":"` + eventType + `","message":"the sender failed"}`)
	}
	return b
}
