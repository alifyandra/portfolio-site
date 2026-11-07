package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/internal/finance"
)

// The Projection read (ADR 0019): GET /api/finance/projection, a forward Balance Series
// per account and for net worth under the premise that nothing changes. It is guarded like
// every other finance read: cookie auth, then requireAdmin as the handler's first line.
// The walk lives in finance.Projection; this file only validates the query and maps the
// result to the wire, rounding every money figure to the cent as the income DTOs do.

// ProjectionPointDTO is one projected bucket. projected is always true: it is on the wire so
// a consumer that merges these points with a history series can never read one as a reading.
type ProjectionPointDTO struct {
	AsOf      string  `json:"as_of" doc:"RFC3339 bucket START in Australia/Melbourne, on the Balance Series grammar (a day runs local midnight to local midnight, a week starts local Monday, a month the local 1st)"`
	Balance   float64 `json:"balance" doc:"Projected close of the bucket, rounded to the cent"`
	In        float64 `json:"in" doc:"Declared money in applied inside the bucket, a positive magnitude"`
	Out       float64 `json:"out" doc:"Declared money out applied inside the bucket, a positive magnitude"`
	Projected bool    `json:"projected" doc:"Always true: this is an extrapolation of declarations, never a reading"`
}

// ProjectionAccountDTO is one account's projected line.
type ProjectionAccountDTO struct {
	AccountID    int                  `json:"account_id"`
	AccountName  string               `json:"account_name"`
	Type         string               `json:"type" enum:"everyday,savings,credit_card,steppay,investment"`
	Class        string               `json:"class" enum:"asset,liability"`
	Currency     string               `json:"currency"`
	StartBalance *float64             `json:"start_balance" doc:"The latest balance snapshot the walk starts from; null when the account has none"`
	StartAsOf    *string              `json:"start_as_of" doc:"RFC3339 reading time of that snapshot; null with it"`
	Flat         bool                 `json:"flat" doc:"True for an investment account, carried flat at its reading because nothing declared moves it"`
	Note         string               `json:"note" doc:"Why the line is empty or flat; empty otherwise"`
	Points       []ProjectionPointDTO `json:"points" doc:"From the snapshot's bucket through the horizon end's bucket"`
}

// ProjectionNetWorthDTO is the net-worth line: every account's line summed, plus the flows
// declared with no account.
type ProjectionNetWorthDTO struct {
	Currency     string               `json:"currency"`
	StartBalance *float64             `json:"start_balance" doc:"Sum of every account's latest snapshot; null when no account has one"`
	StartAsOf    *string              `json:"start_as_of" doc:"RFC3339 time of the freshest of those snapshots; null with it"`
	Note         string               `json:"note" doc:"Why the line is empty; empty otherwise"`
	Points       []ProjectionPointDTO `json:"points" doc:"From the freshest snapshot's bucket through the horizon end's bucket"`
}

// ProjectionEventDTO is one declared flow the walk applied, so the line can say why it moves.
type ProjectionEventDTO struct {
	Date      string  `json:"date" doc:"YYYY-MM-DD occurrence (the derived due or pay date, or a wishlist deadline)"`
	Kind      string  `json:"kind" enum:"bill,income,wishlist"`
	ID        int     `json:"id" doc:"The bill, income or wishlist item id"`
	Name      string  `json:"name"`
	Amount    float64 `json:"amount" doc:"Signed, rounded to the cent: money out negative"`
	AccountID *int    `json:"account_id" doc:"Account the flow moves; null when it moves net worth only"`
}

// ProjectionExclusionDTO is one declaration the walk left out, and why.
type ProjectionExclusionDTO struct {
	Kind   string `json:"kind" enum:"bill,income,wishlist"`
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Reason string `json:"reason" enum:"paused,investment_account,no_snapshot,currency_mismatch" doc:"paused: declared paused. investment_account: names an investment account, which is carried flat. no_snapshot: names an account with no balance reading to start from. currency_mismatch: its currency differs from its account's (or, with no account, from net worth's)"`
}

type financeProjectionInput struct {
	HorizonDays int `query:"horizon_days" default:"90" minimum:"1" maximum:"730" doc:"Days ahead of today (the Melbourne calendar day) to project"`
	// step is validated in the handler, like the balance history's, so an unknown value is
	// a 400 naming the allowed set.
	Step            string `query:"step" doc:"Bucket width: day (default), week or month, on the Balance Series grammar. An unknown value is a 400."`
	IncludeWishlist bool   `query:"include_wishlist" doc:"Also apply each wanted wishlist item that has both a cost and a deadline, as money out on its deadline (net worth only)"`
}

type financeProjectionOutput struct {
	Body struct {
		Today           string                   `json:"today" doc:"YYYY-MM-DD the horizon counts from, on the Melbourne calendar"`
		HorizonEnd      string                   `json:"horizon_end" doc:"YYYY-MM-DD, today plus horizon_days; the last day any flow is applied"`
		HorizonDays     int                      `json:"horizon_days"`
		Step            string                   `json:"step" enum:"day,week,month"`
		IncludeWishlist bool                     `json:"include_wishlist"`
		Accounts        []ProjectionAccountDTO   `json:"accounts" doc:"One line per account, name-ordered"`
		NetWorth        ProjectionNetWorthDTO    `json:"net_worth"`
		Events          []ProjectionEventDTO     `json:"events" doc:"Every flow applied, oldest first"`
		Excluded        []ProjectionExclusionDTO `json:"excluded" doc:"Declarations the walk left out"`
	}
}

func (h *Handler) registerFinanceProjection(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-finance-projection",
		Method:      http.MethodGet,
		Path:        "/api/finance/projection",
		Summary:     "Project balances and net worth forward from declared bills and income (admin)",
		Description: "A forward Balance Series under one premise, nothing changes: from each account's latest " +
			"balance snapshot, apply every active or not-yet-ended recurring bill (out) and recurring income (in) " +
			"occurrence in the horizon, skipping any cycle whose payment already posted on or before that " +
			"account's snapshot. Investment accounts are carried flat and flows with no account move net worth " +
			"only. Every point is marked projected.",
		Tags:     financeReadTags,
		Security: cookieAuthSecurity,
	}, h.getFinanceProjection)
}

func (h *Handler) getFinanceProjection(ctx context.Context, in *financeProjectionInput) (*financeProjectionOutput, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if h.deps.Ent == nil {
		return nil, huma.Error503ServiceUnavailable("finance is not available")
	}
	step, err := finance.ParseBalanceStep(strings.TrimSpace(in.Step))
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	if step == finance.StepRaw {
		step = finance.StepDay
	}

	v, err := finance.Projection(ctx, h.deps.Ent, finance.ProjectionFilter{
		HorizonDays:     in.HorizonDays,
		Step:            step,
		IncludeWishlist: in.IncludeWishlist,
	})
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to compute the projection", err)
	}

	out := &financeProjectionOutput{}
	out.Body.Today = v.Today.Format(dateLayout)
	out.Body.HorizonEnd = v.End.Format(dateLayout)
	out.Body.HorizonDays = v.HorizonDays
	out.Body.Step = string(v.Step)
	out.Body.IncludeWishlist = v.IncludeWishlist

	out.Body.Accounts = make([]ProjectionAccountDTO, 0, len(v.Accounts))
	for _, s := range v.Accounts {
		out.Body.Accounts = append(out.Body.Accounts, ProjectionAccountDTO{
			AccountID:    s.AccountID,
			AccountName:  s.AccountName,
			Type:         s.Type,
			Class:        s.Class,
			Currency:     s.Currency,
			StartBalance: roundCentsPtr(s.Start),
			StartAsOf:    rfc3339Ptr(s.StartAsOf),
			Flat:         s.Flat,
			Note:         s.Note,
			Points:       toProjectionPointDTOs(s.Points),
		})
	}
	out.Body.NetWorth = ProjectionNetWorthDTO{
		Currency:     v.NetWorth.Currency,
		StartBalance: roundCentsPtr(v.NetWorth.Start),
		StartAsOf:    rfc3339Ptr(v.NetWorth.StartAsOf),
		Note:         v.NetWorth.Note,
		Points:       toProjectionPointDTOs(v.NetWorth.Points),
	}

	out.Body.Events = make([]ProjectionEventDTO, 0, len(v.Events))
	for _, e := range v.Events {
		dto := ProjectionEventDTO{
			Date:   e.Date.Format(dateLayout),
			Kind:   e.Kind,
			ID:     e.ID,
			Name:   e.Name,
			Amount: roundCents(e.Amount),
		}
		if e.AccountID != 0 {
			id := e.AccountID
			dto.AccountID = &id
		}
		out.Body.Events = append(out.Body.Events, dto)
	}
	out.Body.Excluded = make([]ProjectionExclusionDTO, 0, len(v.Excluded))
	for _, x := range v.Excluded {
		out.Body.Excluded = append(out.Body.Excluded, ProjectionExclusionDTO{Kind: x.Kind, ID: x.ID, Name: x.Name, Reason: x.Reason})
	}
	return out, nil
}

// toProjectionPointDTOs maps projected buckets to the wire. A bucket start is a local
// midnight, so it is rendered in the bucket zone: its UTC form names the previous day.
func toProjectionPointDTOs(points []finance.ProjectedPoint) []ProjectionPointDTO {
	out := make([]ProjectionPointDTO, 0, len(points))
	for _, p := range points {
		out = append(out, ProjectionPointDTO{
			AsOf:      p.AsOf.In(finance.BucketZone()).Format(time.RFC3339),
			Balance:   roundCents(p.Balance),
			In:        roundCents(p.In),
			Out:       roundCents(p.Out),
			Projected: true,
		})
	}
	return out
}
