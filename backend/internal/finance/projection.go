package finance

import (
	"context"
	"sort"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/billpayment"
	"github.com/alifyandra/portfolio-site/backend/ent/incomereceipt"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringbill"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringincome"
	"github.com/alifyandra/portfolio-site/backend/ent/wishlistitem"
)

// The Projection (ADR 0019): a forward Balance Series under one stated premise, nothing
// changes. It starts from each account's latest Balance Snapshot and walks forward
// applying only what the owner has declared: every Recurring Bill occurrence (money out)
// and Recurring Income occurrence (money in) in the horizon, and, when asked for, each
// wanted Wishlist Item that carries both a cost and a deadline. It is an extrapolation of
// declarations, never a forecast, so nothing here fits a trend or infers a flow from the
// ledger: with nothing declared the projection is a flat line, which is the honest answer.
//
// The rules the walk follows, each of which a test pins:
//
//   - An occurrence counts when it falls AFTER the snapshot's Melbourne day and on or
//     before the horizon end. The snapshot is read as that day's close; a cycle due on or
//     before it is either in the balance already or overdue, and the premise does not guess
//     when an overdue cycle will be paid.
//   - A cycle whose Bill Payment or Income Receipt posted on or before the snapshot day of
//     the account it posted to is skipped: pay that lands early puts a future cycle in the
//     past, and the snapshot already carries it. A cycle paid AFTER that snapshot (a stale
//     snapshot) is still applied, since the reading does not reflect it.
//   - paused declarations apply nothing. ended_on caps occurrences whatever the status, so
//     an income declared ended on a future date runs until that date; an ended declaration
//     with no ended_on applies nothing.
//   - An investment account is carried flat, since nothing declared moves it, and a
//     declaration naming one is left out rather than moving it.
//   - A declaration with no account moves net worth only. Net worth is the sum of every
//     account's line plus those unassigned flows.
//   - A wishlist item is one flow on its deadline, moving net worth only. One due on or
//     before the freshest reading's day is reported as past_deadline, not applied.
//   - Every line opens at today's bucket, or at its reading's bucket when that is later. A
//     flow between a stale reading and today still applies and is still listed as an
//     event, but it is folded into the opening close rather than given a point of its own,
//     so a reading a year old does not add a year of points.
//
// Buckets use the Balance Series grammar exactly (bucketStart / nextBucketStart: local
// Melbourne day, Monday week, 1st-of-month), and a bucket's value is its close. Nothing
// here rounds money; the API rounds at its DTO boundary.

// Horizon bounds for a projection, in days from today. The cap keeps a weekly declaration
// to about a hundred occurrences ahead of today and a daily series to a few hundred
// points, however stale a reading is, since no line opens before today's bucket.
const (
	DefaultProjectionHorizonDays = 90
	MaxProjectionHorizonDays     = 730
)

// What an applied flow came from.
const (
	ProjectionKindBill     = "bill"
	ProjectionKindIncome   = "income"
	ProjectionKindWishlist = "wishlist"
)

// Why a declaration contributes nothing to the projection. Reported rather than dropped
// silently, because a projection is only as complete as what was declared and the owner
// needs to see which declarations it left out.
const (
	ExcludedPaused           = "paused"
	ExcludedInvestment       = "investment_account"
	ExcludedNoSnapshot       = "no_snapshot"
	ExcludedCurrencyMismatch = "currency_mismatch"
	// ExcludedPastDeadline is a wanted wishlist item whose deadline falls on or before the
	// freshest reading's day, where net worth's walk starts: it is overdue or already due,
	// and the premise does not guess when it will be bought.
	ExcludedPastDeadline = "past_deadline"
)

// ProjectionFilter shapes one projection. HorizonDays counts forward from today (zero or
// less means the default, more than the cap is clamped). Step is a Balance Series step;
// StepRaw means day, since a projection has no readings to list. IncludeWishlist adds the
// wanted, priced, dated wishlist items. Now is the instant "today" is read from; zero means
// the wall clock, and a test pins it.
type ProjectionFilter struct {
	HorizonDays     int
	Step            BalanceStep
	IncludeWishlist bool
	Now             time.Time
}

// ProjectedPoint is one bucket of a projected line. AsOf is the bucket START as a local-zone
// instant, as on a Balance Series. Balance is the bucket's close. In and Out are the declared
// money applied inside the bucket, both positive magnitudes. Every ProjectedPoint is
// projected by construction; the API marks it so on the wire.
type ProjectedPoint struct {
	AsOf    time.Time
	Balance float64
	In      float64
	Out     float64
}

// ProjectedSeries is one account's projected line, or (with AccountID 0) net worth's.
// Start and StartAsOf are the reading the walk starts from: the account's latest snapshot,
// or for net worth the sum of them stamped with the freshest. Both are nil, with Points
// empty and Note saying why, when there is no reading to start from. Flat marks an
// investment account, carried at its reading.
type ProjectedSeries struct {
	AccountID   int
	AccountName string
	Type        string
	Class       string
	Currency    string
	Start       *float64
	StartAsOf   *time.Time
	Flat        bool
	Points      []ProjectedPoint
	Note        string
}

// ProjectionEvent is one declared flow the walk applied: the reason a line moves. Date is
// the occurrence (UTC midnight, the form every stored finance date takes), Amount is signed
// (money out negative), and AccountID is 0 for a flow that moves net worth only.
type ProjectionEvent struct {
	Date      time.Time
	Kind      string
	ID        int
	Name      string
	Amount    float64
	AccountID int
}

// ProjectionExclusion names a declaration the walk left out, and why.
type ProjectionExclusion struct {
	Kind   string
	ID     int
	Name   string
	Reason string
}

// ProjectionView is the whole projection: one line per account, the net-worth line, every
// applied flow and every declaration left out. Today and End are UTC-midnight dates on the
// Melbourne calendar; End is Today plus the horizon.
type ProjectionView struct {
	Today           time.Time
	End             time.Time
	HorizonDays     int
	Step            BalanceStep
	IncludeWishlist bool
	Accounts        []ProjectedSeries
	NetWorth        ProjectedSeries
	Events          []ProjectionEvent
	Excluded        []ProjectionExclusion
}

// projectionAnchor is the reading one account's walk starts from. day is the snapshot's
// Melbourne calendar day as a UTC-midnight date, which is what occurrences and posted dates
// compare against.
type projectionAnchor struct {
	balance float64
	asOf    time.Time
	day     time.Time
	flat    bool
}

// projectedFlow is one declaration reduced to what the walk needs. sign is -1 for money
// out and +1 for money in. A wishlist item is a single occurrence on its deadline (once is
// true); everything else steps its cadence from anchor.
type projectedFlow struct {
	kind      string
	id        int
	name      string
	amount    float64
	sign      float64
	cadence   recurringbill.Cadence
	anchor    time.Time
	endedOn   *time.Time
	accountID int
	once      bool
}

// settlement is the posted row that paid one cycle: when it posted and to which account.
type settlement struct {
	posted    time.Time
	accountID int
}

// cycleKey identifies one cycle of one declaration in the settled set.
type cycleKey struct {
	kind string
	id   int
	day  int64
}

// Projection computes the projection described at the top of this file.
func Projection(ctx context.Context, client *ent.Client, f ProjectionFilter) (ProjectionView, error) {
	if f.Step == StepRaw {
		f.Step = StepDay
	}
	switch {
	case f.HorizonDays <= 0:
		f.HorizonDays = DefaultProjectionHorizonDays
	case f.HorizonDays > MaxProjectionHorizonDays:
		f.HorizonDays = MaxProjectionHorizonDays
	}
	now := f.Now
	if now.IsZero() {
		now = time.Now()
	}
	today := LocalDate(now)
	v := ProjectionView{
		Today:           today,
		End:             today.AddDate(0, 0, f.HorizonDays),
		HorizonDays:     f.HorizonDays,
		Step:            f.Step,
		IncludeWishlist: f.IncludeWishlist,
		Accounts:        []ProjectedSeries{},
		NetWorth:        ProjectedSeries{Currency: reportCurrency, Points: []ProjectedPoint{}},
		Events:          []ProjectionEvent{},
		Excluded:        []ProjectionExclusion{},
	}

	accs, err := client.Account.Query().Order(ent.Asc(account.FieldName)).All(ctx)
	if err != nil {
		return v, err
	}
	anchors := make(map[int]*projectionAnchor, len(accs))
	currency := make(map[int]string, len(accs))
	var newest *projectionAnchor
	for _, acc := range accs {
		currency[acc.ID] = acc.Currency
		snap, err := latestSnapshot(ctx, client, acc.ID)
		if err != nil {
			return v, err
		}
		if snap == nil {
			continue
		}
		a := &projectionAnchor{
			balance: snap.Balance,
			asOf:    snap.AsOf,
			day:     LocalDate(snap.AsOf),
			flat:    acc.Type == account.TypeInvestment,
		}
		anchors[acc.ID] = a
		if newest == nil || a.asOf.After(newest.asOf) {
			newest = a
		}
	}
	if newest == nil {
		for _, acc := range accs {
			v.Accounts = append(v.Accounts, noSnapshotSeries(acc))
		}
		v.NetWorth.Note = "no account has a balance snapshot, so there is nothing to project from"
		return v, nil
	}

	flows, excluded, err := loadProjectedFlows(ctx, client, f.IncludeWishlist, newest.day, anchors, currency)
	if err != nil {
		return v, err
	}
	v.Excluded = append(v.Excluded, excluded...)

	floor := newest.day
	for _, a := range anchors {
		if a.day.Before(floor) {
			floor = a.day
		}
	}
	settled, err := loadSettlements(ctx, client, floor)
	if err != nil {
		return v, err
	}

	for _, fl := range flows {
		// A flow with no account starts where net worth does: the freshest reading.
		lower := newest.day
		if fl.accountID != 0 {
			lower = anchors[fl.accountID].day
		}
		for _, occ := range fl.occurrences(lower, v.End) {
			if s, ok := settled[cycleKey{fl.kind, fl.id, occ.Unix()}]; ok && reflectedIn(s, anchors) {
				continue
			}
			v.Events = append(v.Events, ProjectionEvent{
				Date:      occ,
				Kind:      fl.kind,
				ID:        fl.id,
				Name:      fl.name,
				Amount:    fl.sign * fl.amount,
				AccountID: fl.accountID,
			})
		}
	}
	sort.SliceStable(v.Events, func(i, j int) bool {
		a, b := v.Events[i], v.Events[j]
		if !a.Date.Equal(b.Date) {
			return a.Date.Before(b.Date)
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})

	byAccount := make(map[int][]ProjectionEvent, len(anchors))
	for _, e := range v.Events {
		byAccount[e.AccountID] = append(byAccount[e.AccountID], e)
	}

	hi := bucketStart(v.End, f.Step)
	todayBucket := bucketStart(v.Today, f.Step)
	// One bucket index per account line, in account order so the net-worth sum adds in
	// the same order on every request.
	closes := make([]map[int64]ProjectedPoint, 0, len(anchors))
	var nwStart float64
	for _, acc := range accs {
		a, ok := anchors[acc.ID]
		if !ok {
			v.Accounts = append(v.Accounts, noSnapshotSeries(acc))
			continue
		}
		start, asOf := a.balance, a.asOf
		s := ProjectedSeries{
			AccountID:   acc.ID,
			AccountName: acc.Name,
			Type:        string(acc.Type),
			Class:       string(acc.Class),
			Currency:    acc.Currency,
			Start:       &start,
			StartAsOf:   &asOf,
			Flat:        a.flat,
		}
		// An investment account never receives a flow (loadProjectedFlows leaves out any
		// declaration naming one), so walking it with no events is what carries it flat.
		s.Points = walkBuckets(laterOf(bucketStart(a.asOf, f.Step), todayBucket), hi, f.Step, a.balance, byAccount[acc.ID])
		if a.flat {
			s.Note = "investment account carried flat: nothing declared moves it"
		}
		idx := make(map[int64]ProjectedPoint, len(s.Points))
		for _, p := range s.Points {
			idx[bucketKey(p.AsOf)] = p
		}
		closes = append(closes, idx)
		nwStart += a.balance
		v.Accounts = append(v.Accounts, s)
	}

	// Net worth opens at the freshest reading's bucket, or today's when that is later:
	// every account's line already reaches that far (each opens at its own, older or
	// equal, reading's bucket or at today's), so every bucket from here sums a defined
	// close per account and nothing is back-filled.
	nwLo := laterOf(bucketStart(newest.asOf, f.Step), todayBucket)
	nwAsOf := newest.asOf
	v.NetWorth.Start = &nwStart
	v.NetWorth.StartAsOf = &nwAsOf
	unassigned := walkBuckets(nwLo, hi, f.Step, 0, byAccount[0])
	for _, u := range unassigned {
		k := bucketKey(u.AsOf)
		p := ProjectedPoint{AsOf: u.AsOf, Balance: u.Balance, In: u.In, Out: u.Out}
		for _, idx := range closes {
			c := idx[k]
			p.Balance += c.Balance
			p.In += c.In
			p.Out += c.Out
		}
		v.NetWorth.Points = append(v.NetWorth.Points, p)
	}
	return v, nil
}

// noSnapshotSeries is the line for an account that has never carried a reading: there is
// no level to walk from, so it is empty and says so rather than starting at zero.
func noSnapshotSeries(acc *ent.Account) ProjectedSeries {
	return ProjectedSeries{
		AccountID:   acc.ID,
		AccountName: acc.Name,
		Type:        string(acc.Type),
		Class:       string(acc.Class),
		Currency:    acc.Currency,
		Flat:        acc.Type == account.TypeInvestment,
		Points:      []ProjectedPoint{},
		Note:        "no balance snapshot to project from",
	}
}

// walkBuckets emits one point per bucket in [lo, hi], each closing at start plus every
// event up to the end of that bucket. An event in a bucket before lo (a flow between a
// stale reading and today) is folded into the opening level instead, so the first point
// already carries it and the point count depends only on lo and hi.
func walkBuckets(lo, hi time.Time, step BalanceStep, start float64, events []ProjectionEvent) []ProjectedPoint {
	if hi.Before(lo) {
		hi = lo
	}
	level := start
	in := make(map[int64]float64)
	out := make(map[int64]float64)
	for _, e := range events {
		b := bucketStart(e.Date, step)
		if b.Before(lo) {
			level += e.Amount
			continue
		}
		k := bucketKey(b)
		if e.Amount >= 0 {
			in[k] += e.Amount
		} else {
			out[k] -= e.Amount
		}
	}
	points := []ProjectedPoint{}
	for b := lo; !b.After(hi); b = nextBucketStart(b, step) {
		k := bucketKey(b)
		level += in[k] - out[k]
		points = append(points, ProjectedPoint{AsOf: b, Balance: level, In: in[k], Out: out[k]})
	}
	return points
}

// laterOf is the later of two instants.
func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// occurrences lists the flow's dates in (after, end]: strictly after the day its walk starts
// from, on or before the horizon end, never before its anchor and never after ended_on.
func (fl projectedFlow) occurrences(after, end time.Time) []time.Time {
	from := after.AddDate(0, 0, 1)
	if fl.once {
		d := normalizeDate(fl.anchor)
		if d.Before(from) || d.After(end) {
			return nil
		}
		return []time.Time{d}
	}
	if a := normalizeDate(fl.anchor); a.After(from) {
		from = a
	}
	to := end
	if fl.endedOn != nil {
		if e := normalizeDate(*fl.endedOn); e.Before(to) {
			to = e
		}
	}
	return occurrencesBetween(fl.anchor, fl.cadence, from, to)
}

// reflectedIn reports whether the row that paid a cycle is already inside a reading: it
// posted on or before the snapshot day of the account it posted to. An account with no
// reading reflects nothing.
func reflectedIn(s settlement, anchors map[int]*projectionAnchor) bool {
	a, ok := anchors[s.accountID]
	return ok && !normalizeDate(s.posted).After(a.day)
}

// loadProjectedFlows reads every declaration and reduces the ones the walk applies to
// projectedFlows, reporting the rest. A declaration on an account must name one with a
// reading that is not an investment, in that account's currency; one with no account moves
// net worth, which is reported in reportCurrency, so it has to be in that currency. A
// wishlist item moves net worth only, whose walk starts after netWorthDay (the freshest
// reading's day), so an item due on or before that day is reported as past its deadline.
func loadProjectedFlows(ctx context.Context, client *ent.Client, includeWishlist bool, netWorthDay time.Time, anchors map[int]*projectionAnchor, currency map[int]string) ([]projectedFlow, []ProjectionExclusion, error) {
	var flows []projectedFlow
	var excluded []ProjectionExclusion

	// admit applies the account and currency rules shared by bills and income.
	admit := func(fl projectedFlow, cur string) {
		if reason := flowExclusion(fl.accountID, cur, anchors, currency); reason != "" {
			excluded = append(excluded, ProjectionExclusion{Kind: fl.kind, ID: fl.id, Name: fl.name, Reason: reason})
			return
		}
		flows = append(flows, fl)
	}

	bills, err := client.RecurringBill.Query().WithAccount().Order(ent.Asc(recurringbill.FieldName)).All(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, b := range bills {
		if b.Status == recurringbill.StatusPaused {
			excluded = append(excluded, ProjectionExclusion{Kind: ProjectionKindBill, ID: b.ID, Name: b.Name, Reason: ExcludedPaused})
			continue
		}
		if b.Status == recurringbill.StatusEnded && b.EndedOn == nil {
			continue // finished at an unknown date: nothing is expected
		}
		fl := projectedFlow{
			kind: ProjectionKindBill, id: b.ID, name: b.Name, amount: b.ExpectedAmount, sign: -1,
			cadence: b.Cadence, anchor: b.AnchorDate, endedOn: b.EndedOn,
		}
		if a := b.Edges.Account; a != nil {
			fl.accountID = a.ID
		}
		admit(fl, b.Currency)
	}

	incomes, err := client.RecurringIncome.Query().WithAccount().Order(ent.Asc(recurringincome.FieldName)).All(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, inc := range incomes {
		if inc.Status == recurringincome.StatusPaused {
			excluded = append(excluded, ProjectionExclusion{Kind: ProjectionKindIncome, ID: inc.ID, Name: inc.Name, Reason: ExcludedPaused})
			continue
		}
		if inc.Status == recurringincome.StatusEnded && inc.EndedOn == nil {
			continue
		}
		fl := projectedFlow{
			kind: ProjectionKindIncome, id: inc.ID, name: inc.Name, amount: inc.ExpectedAmount, sign: 1,
			cadence: billCadence(inc.Cadence), anchor: inc.AnchorDate, endedOn: inc.EndedOn,
		}
		if a := inc.Edges.Account; a != nil {
			fl.accountID = a.ID
		}
		admit(fl, inc.Currency)
	}

	if includeWishlist {
		items, err := client.WishlistItem.Query().
			Where(
				wishlistitem.StatusEQ(wishlistitem.StatusWanted),
				wishlistitem.AmountNotNil(),
				wishlistitem.DeadlineNotNil(),
			).
			Order(ent.Asc(wishlistitem.FieldDeadline), ent.Asc(wishlistitem.FieldID)).
			All(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, it := range items {
			if !normalizeDate(*it.Deadline).After(netWorthDay) {
				excluded = append(excluded, ProjectionExclusion{Kind: ProjectionKindWishlist, ID: it.ID, Name: it.Name, Reason: ExcludedPastDeadline})
				continue
			}
			admit(projectedFlow{
				kind: ProjectionKindWishlist, id: it.ID, name: it.Name, amount: *it.Amount, sign: -1,
				anchor: *it.Deadline, once: true,
			}, it.Currency)
		}
	}
	return flows, excluded, nil
}

// flowExclusion is the reason a declaration on accountID (0 for none) in currency cur is
// left out, or "" when the walk applies it.
func flowExclusion(accountID int, cur string, anchors map[int]*projectionAnchor, currency map[int]string) string {
	if accountID == 0 {
		if cur != reportCurrency {
			return ExcludedCurrencyMismatch
		}
		return ""
	}
	a, ok := anchors[accountID]
	switch {
	case !ok:
		return ExcludedNoSnapshot
	case a.flat:
		return ExcludedInvestment
	case cur != currency[accountID]:
		return ExcludedCurrencyMismatch
	}
	return ""
}

// loadSettlements reads every bill payment and income receipt for a cycle after floor (the
// oldest reading's day, below which no walk reaches) with the row that paid it.
func loadSettlements(ctx context.Context, client *ent.Client, floor time.Time) (map[cycleKey]settlement, error) {
	out := make(map[cycleKey]settlement)
	payments, err := client.BillPayment.Query().
		Where(billpayment.OccurrenceDateGT(floor)).
		WithBill().
		WithTransaction(func(q *ent.TransactionQuery) { q.WithAccount() }).
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range payments {
		if p.Edges.Bill == nil {
			continue
		}
		if s, ok := settlementOf(p.Edges.Transaction); ok {
			out[cycleKey{ProjectionKindBill, p.Edges.Bill.ID, normalizeDate(p.OccurrenceDate).Unix()}] = s
		}
	}
	receipts, err := client.IncomeReceipt.Query().
		Where(incomereceipt.OccurrenceDateGT(floor)).
		WithIncome().
		WithTransaction(func(q *ent.TransactionQuery) { q.WithAccount() }).
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range receipts {
		if r.Edges.Income == nil {
			continue
		}
		if s, ok := settlementOf(r.Edges.Transaction); ok {
			out[cycleKey{ProjectionKindIncome, r.Edges.Income.ID, normalizeDate(r.OccurrenceDate).Unix()}] = s
		}
	}
	return out, nil
}

// settlementOf reads the posted date and account off the row that paid a cycle.
func settlementOf(t *ent.Transaction) (settlement, bool) {
	if t == nil || t.Edges.Account == nil {
		return settlement{}, false
	}
	return settlement{posted: t.PostedDate, accountID: t.Edges.Account.ID}, true
}
