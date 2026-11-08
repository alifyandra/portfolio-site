package finance

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringbill"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringincome"
	"github.com/alifyandra/portfolio-site/backend/ent/wishlistitem"
)

// Projection walk tests (ADR 0019). Every case runs against a pinned clock, so none of
// them moves with the wall clock or with the gap between the UTC and Melbourne days. All
// names and amounts are invented.

// projNow is the pinned instant the walk cases are measured from: noon on Sunday
// 8 November 2026 in Melbourne (AEDT, +11:00), so "today" is the 8th.
var projNow = mel(2026, time.November, 8, 12)

// projFixture is what every walk case starts from: one everyday account read at 9am on
// the 8th at 1000.
type projFixture struct {
	everyday *ent.Account
}

func onAccount(acc *ent.Account) func(*ent.RecurringBillCreate) {
	return func(c *ent.RecurringBillCreate) { c.SetAccountID(acc.ID) }
}

func incomeOn(acc *ent.Account) func(*ent.RecurringIncomeCreate) {
	return func(c *ent.RecurringIncomeCreate) { c.SetAccountID(acc.ID) }
}

// seedProjTxn posts one row and returns it, so a cycle can be linked to it.
func seedProjTxn(t *testing.T, ctx context.Context, client *ent.Client, acc *ent.Account, tag string, posted time.Time, amount float64) *ent.Transaction {
	t.Helper()
	return client.Transaction.Create().
		SetDedupHash("proj-" + tag).
		SetPostedDate(posted).
		SetAmount(amount).
		SetDescription("PROJ " + tag).
		SetAccountID(acc.ID).
		SaveX(ctx)
}

// eventStrings renders applied flows as "date kind name amount" for comparison.
func eventStrings(events []ProjectionEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, fmt.Sprintf("%s %s %s %g", e.Date.Format("2006-01-02"), e.Kind, e.Name, e.Amount))
	}
	return out
}

func exclusionStrings(ex []ProjectionExclusion) []string {
	out := make([]string, 0, len(ex))
	for _, e := range ex {
		out = append(out, fmt.Sprintf("%s %s %s", e.Kind, e.Name, e.Reason))
	}
	slices.Sort(out)
	return out
}

// seriesByName finds an account's projected line; the empty name is net worth.
func seriesByName(t *testing.T, v ProjectionView, name string) ProjectedSeries {
	t.Helper()
	if name == "" {
		return v.NetWorth
	}
	for _, s := range v.Accounts {
		if s.AccountName == name {
			return s
		}
	}
	t.Fatalf("no projected series for account %q", name)
	return ProjectedSeries{}
}

func lastClose(t *testing.T, s ProjectedSeries) float64 {
	t.Helper()
	if len(s.Points) == 0 {
		t.Fatalf("series %q has no points", s.AccountName)
	}
	return s.Points[len(s.Points)-1].Balance
}

// TestProjection_Walk is the walk's rulebook, one case per rule. wantEnd maps an account
// name to the close of its last bucket, with "" standing for net worth; wantExcluded is
// sorted.
func TestProjection_Walk(t *testing.T) {
	cases := []struct {
		name         string
		horizon      int
		step         BalanceStep
		wishlist     bool
		seed         func(t *testing.T, ctx context.Context, c *ent.Client, f projFixture)
		wantEvents   []string
		wantEnd      map[string]float64
		wantExcluded []string
		check        func(t *testing.T, v ProjectionView)
	}{
		{
			name:    "no declarations is a flat line",
			horizon: 14,
			seed:    func(*testing.T, context.Context, *ent.Client, projFixture) {},
			wantEnd: map[string]float64{"Everyday": 1000, "": 1000},
			check: func(t *testing.T, v ProjectionView) {
				s := seriesByName(t, v, "Everyday")
				// The snapshot's own day through today + 14, inclusive.
				if len(s.Points) != 15 {
					t.Fatalf("points = %d, want 15", len(s.Points))
				}
				if !s.Points[0].AsOf.Equal(mel(2026, time.November, 8, 0)) {
					t.Errorf("first bucket = %v, want the snapshot's local day", s.Points[0].AsOf)
				}
			},
		},
		{
			name:    "weekly bill, the cycle on the snapshot day is already in the reading",
			horizon: 28,
			seed: func(t *testing.T, ctx context.Context, c *ent.Client, f projFixture) {
				seedBill(t, ctx, c, "Groceries", 50, recurringbill.CadenceWeekly, utcDay(2026, time.November, 1), onAccount(f.everyday))
			},
			wantEvents: []string{
				"2026-11-15 bill Groceries -50",
				"2026-11-22 bill Groceries -50",
				"2026-11-29 bill Groceries -50",
				"2026-12-06 bill Groceries -50",
			},
			wantEnd: map[string]float64{"Everyday": 800, "": 800},
		},
		{
			name:    "fortnightly income",
			horizon: 28,
			seed: func(t *testing.T, ctx context.Context, c *ent.Client, f projFixture) {
				seedIncome(t, ctx, c, "Salary", 2000, recurringincome.CadenceFortnightly, utcDay(2026, time.October, 30), incomeOn(f.everyday))
			},
			wantEvents: []string{
				"2026-11-13 income Salary 2000",
				"2026-11-27 income Salary 2000",
			},
			wantEnd: map[string]float64{"Everyday": 5000, "": 5000},
		},
		{
			name:    "monthly bill anchored on the 31st clamps to each month end",
			horizon: 120,
			seed: func(t *testing.T, ctx context.Context, c *ent.Client, f projFixture) {
				seedBill(t, ctx, c, "Rent", 1000, recurringbill.CadenceMonthly, utcDay(2026, time.January, 31), onAccount(f.everyday))
			},
			wantEvents: []string{
				"2026-11-30 bill Rent -1000",
				"2026-12-31 bill Rent -1000",
				"2027-01-31 bill Rent -1000",
				"2027-02-28 bill Rent -1000",
			},
			wantEnd: map[string]float64{"Everyday": -3000, "": -3000},
		},
		{
			name:    "month step buckets on the local 1st, each closing at its last flow",
			horizon: 120,
			step:    StepMonth,
			seed: func(t *testing.T, ctx context.Context, c *ent.Client, f projFixture) {
				seedBill(t, ctx, c, "Rent", 1000, recurringbill.CadenceMonthly, utcDay(2026, time.January, 31), onAccount(f.everyday))
			},
			wantEvents: []string{
				"2026-11-30 bill Rent -1000",
				"2026-12-31 bill Rent -1000",
				"2027-01-31 bill Rent -1000",
				"2027-02-28 bill Rent -1000",
			},
			wantEnd: map[string]float64{"Everyday": -3000, "": -3000},
			check: func(t *testing.T, v ProjectionView) {
				s := seriesByName(t, v, "Everyday")
				wantStarts := []time.Time{
					mel(2026, time.November, 1, 0), mel(2026, time.December, 1, 0), mel(2027, time.January, 1, 0),
					mel(2027, time.February, 1, 0), mel(2027, time.March, 1, 0),
				}
				wantCloses := []float64{0, -1000, -2000, -3000, -3000}
				if len(s.Points) != len(wantStarts) {
					t.Fatalf("points = %d, want %d", len(s.Points), len(wantStarts))
				}
				for i, p := range s.Points {
					if !p.AsOf.Equal(wantStarts[i]) || !near(p.Balance, wantCloses[i]) {
						t.Errorf("point %d = %v %v, want %v %v", i, p.AsOf, p.Balance, wantStarts[i], wantCloses[i])
					}
				}
				if !near(s.Points[1].Out, 1000) || s.Points[4].Out != 0 {
					t.Errorf("out = %v / %v, want 1000 in December and none in March", s.Points[1].Out, s.Points[4].Out)
				}
			},
		},
		{
			name:    "week step buckets on local Mondays",
			horizon: 28,
			step:    StepWeek,
			seed: func(t *testing.T, ctx context.Context, c *ent.Client, f projFixture) {
				seedBill(t, ctx, c, "Groceries", 50, recurringbill.CadenceWeekly, utcDay(2026, time.November, 12), onAccount(f.everyday))
			},
			wantEvents: []string{
				"2026-11-12 bill Groceries -50",
				"2026-11-19 bill Groceries -50",
				"2026-11-26 bill Groceries -50",
				"2026-12-03 bill Groceries -50",
			},
			wantEnd: map[string]float64{"Everyday": 800, "": 800},
			check: func(t *testing.T, v ProjectionView) {
				s := seriesByName(t, v, "Everyday")
				wantCloses := []float64{1000, 950, 900, 850, 800}
				if len(s.Points) != len(wantCloses) {
					t.Fatalf("points = %d, want %d", len(s.Points), len(wantCloses))
				}
				// The snapshot falls on a Sunday, so its bucket is the week of Monday the 2nd.
				for i, p := range s.Points {
					want := mel(2026, time.November, 2+7*i, 0)
					if !p.AsOf.Equal(want) || !near(p.Balance, wantCloses[i]) {
						t.Errorf("point %d = %v %v, want %v %v", i, p.AsOf, p.Balance, want, wantCloses[i])
					}
				}
			},
		},
		{
			name:    "quarterly bill, and a daily walk across the April DST change keeps one point per day",
			horizon: 200,
			seed: func(t *testing.T, ctx context.Context, c *ent.Client, f projFixture) {
				seedBill(t, ctx, c, "Insurance", 300, recurringbill.CadenceQuarterly, utcDay(2026, time.August, 15), onAccount(f.everyday))
			},
			wantEvents: []string{
				"2026-11-15 bill Insurance -300",
				"2027-02-15 bill Insurance -300",
				"2027-05-15 bill Insurance -300",
			},
			wantEnd: map[string]float64{"Everyday": 100, "": 100},
			check: func(t *testing.T, v ProjectionView) {
				if got := len(seriesByName(t, v, "Everyday").Points); got != 201 {
					t.Errorf("daily points = %d, want 201 (8 Nov through 27 May)", got)
				}
			},
		},
		{
			name:    "annual bills, a leap-day anchor clamps to 28 February",
			horizon: 365,
			seed: func(t *testing.T, ctx context.Context, c *ent.Client, f projFixture) {
				seedBill(t, ctx, c, "Registration", 1200, recurringbill.CadenceAnnual, utcDay(2025, time.December, 1), onAccount(f.everyday))
				seedBill(t, ctx, c, "Domain", 20, recurringbill.CadenceAnnual, utcDay(2024, time.February, 29), onAccount(f.everyday))
			},
			wantEvents: []string{
				"2026-12-01 bill Registration -1200",
				"2027-02-28 bill Domain -20",
			},
			wantEnd: map[string]float64{"Everyday": -220, "": -220},
		},
		{
			name:    "a cycle paid early, on or before the snapshot, is not counted twice",
			horizon: 28,
			seed: func(t *testing.T, ctx context.Context, c *ent.Client, f projFixture) {
				salary := seedIncome(t, ctx, c, "Salary", 2000, recurringincome.CadenceFortnightly, utcDay(2026, time.November, 9), incomeOn(f.everyday))
				pay := seedProjTxn(t, ctx, c, f.everyday, "salary", utcDay(2026, time.November, 6), 2000)
				c.IncomeReceipt.Create().SetIncomeID(salary.ID).SetTransactionID(pay.ID).SetOccurrenceDate(utcDay(2026, time.November, 9)).SaveX(ctx)

				phone := seedBill(t, ctx, c, "Phone", 40, recurringbill.CadenceMonthly, utcDay(2026, time.October, 10), onAccount(f.everyday))
				debit := seedProjTxn(t, ctx, c, f.everyday, "phone", utcDay(2026, time.November, 8), -40)
				c.BillPayment.Create().SetBillID(phone.ID).SetTransactionID(debit.ID).SetOccurrenceDate(utcDay(2026, time.November, 10)).SaveX(ctx)
			},
			wantEvents: []string{"2026-11-23 income Salary 2000"},
			wantEnd:    map[string]float64{"Everyday": 3000, "": 3000},
		},
		{
			name:    "a stale snapshot applies a cycle that posted after it",
			horizon: 28,
			seed: func(t *testing.T, ctx context.Context, c *ent.Client, f projFixture) {
				saver := seedAccount(t, ctx, c, "Saver", account.ClassAsset, account.TypeSavings)
				seedSnapshot(t, ctx, c, saver, 500, mel(2026, time.November, 3, 9))
				gym := seedBill(t, ctx, c, "Gym", 30, recurringbill.CadenceWeekly, utcDay(2026, time.November, 6), onAccount(saver))
				debit := seedProjTxn(t, ctx, c, saver, "gym", utcDay(2026, time.November, 6), -30)
				c.BillPayment.Create().SetBillID(gym.ID).SetTransactionID(debit.ID).SetOccurrenceDate(utcDay(2026, time.November, 6)).SaveX(ctx)
			},
			wantEvents: []string{
				"2026-11-06 bill Gym -30",
				"2026-11-13 bill Gym -30",
				"2026-11-20 bill Gym -30",
				"2026-11-27 bill Gym -30",
				"2026-12-04 bill Gym -30",
			},
			wantEnd: map[string]float64{"Everyday": 1000, "Saver": 350, "": 1350},
			check: func(t *testing.T, v ProjectionView) {
				saver := seriesByName(t, v, "Saver")
				if !saver.Points[0].AsOf.Equal(mel(2026, time.November, 3, 0)) {
					t.Errorf("saver starts %v, want its own reading's day", saver.Points[0].AsOf)
				}
				// Net worth opens at the freshest reading's day and already carries the
				// stale account's walk up to it.
				nw := v.NetWorth
				if !nw.Points[0].AsOf.Equal(mel(2026, time.November, 8, 0)) || !near(nw.Points[0].Balance, 1470) {
					t.Errorf("net worth opens %v at %v, want 8 Nov at 1470", nw.Points[0].AsOf, nw.Points[0].Balance)
				}
				if nw.Start == nil || *nw.Start != 1500 {
					t.Errorf("net worth start = %v, want the readings' sum 1500", nw.Start)
				}
			},
		},
		{
			name:    "ended_on caps occurrences mid-horizon, whatever the status",
			horizon: 28,
			seed: func(t *testing.T, ctx context.Context, c *ent.Client, f projFixture) {
				seedIncome(t, ctx, c, "Contract", 500, recurringincome.CadenceWeekly, utcDay(2026, time.November, 2), incomeOn(f.everyday),
					func(cr *ent.RecurringIncomeCreate) {
						cr.SetStatus(recurringincome.StatusEnded).SetEndedOn(utcDay(2026, time.November, 20))
					})
				seedBill(t, ctx, c, "Lease", 100, recurringbill.CadenceWeekly, utcDay(2026, time.November, 2), onAccount(f.everyday),
					func(cr *ent.RecurringBillCreate) { cr.SetEndedOn(utcDay(2026, time.November, 17)) })
				seedBill(t, ctx, c, "Old plan", 10, recurringbill.CadenceWeekly, utcDay(2026, time.November, 2), onAccount(f.everyday),
					func(cr *ent.RecurringBillCreate) { cr.SetStatus(recurringbill.StatusEnded) })
				seedBill(t, ctx, c, "Done", 10, recurringbill.CadenceWeekly, utcDay(2026, time.November, 2), onAccount(f.everyday),
					func(cr *ent.RecurringBillCreate) {
						cr.SetStatus(recurringbill.StatusEnded).SetEndedOn(utcDay(2026, time.October, 1))
					})
			},
			wantEvents: []string{
				"2026-11-09 bill Lease -100",
				"2026-11-09 income Contract 500",
				"2026-11-16 bill Lease -100",
				"2026-11-16 income Contract 500",
			},
			wantEnd: map[string]float64{"Everyday": 1800, "": 1800},
		},
		{
			name:    "paused declarations apply nothing and are reported",
			horizon: 28,
			seed: func(t *testing.T, ctx context.Context, c *ent.Client, f projFixture) {
				seedBill(t, ctx, c, "Streaming", 15, recurringbill.CadenceMonthly, utcDay(2026, time.November, 20), onAccount(f.everyday),
					func(cr *ent.RecurringBillCreate) { cr.SetStatus(recurringbill.StatusPaused) })
				seedIncome(t, ctx, c, "Tutoring", 100, recurringincome.CadenceWeekly, utcDay(2026, time.November, 10),
					func(cr *ent.RecurringIncomeCreate) { cr.SetStatus(recurringincome.StatusPaused) })
			},
			wantEnd:      map[string]float64{"Everyday": 1000, "": 1000},
			wantExcluded: []string{"bill Streaming paused", "income Tutoring paused"},
		},
		{
			name:    "a declaration with no account moves net worth only",
			horizon: 60,
			seed: func(t *testing.T, ctx context.Context, c *ent.Client, f projFixture) {
				seedBill(t, ctx, c, "Council rates", 400, recurringbill.CadenceQuarterly, utcDay(2026, time.September, 20))
				seedIncome(t, ctx, c, "Gift", 100, recurringincome.CadenceMonthly, utcDay(2026, time.November, 25))
			},
			wantEvents: []string{
				"2026-11-25 income Gift 100",
				"2026-12-20 bill Council rates -400",
				"2026-12-25 income Gift 100",
			},
			wantEnd: map[string]float64{"Everyday": 1000, "": 800},
			check: func(t *testing.T, v ProjectionView) {
				for _, e := range v.Events {
					if e.AccountID != 0 {
						t.Errorf("event %s carries account %d, want none", e.Name, e.AccountID)
					}
				}
			},
		},
		{
			name:    "an investment account is carried flat and a liability walks down",
			horizon: 28,
			seed: func(t *testing.T, ctx context.Context, c *ent.Client, f projFixture) {
				shares := seedAccount(t, ctx, c, "Shares", account.ClassAsset, account.TypeInvestment)
				seedSnapshot(t, ctx, c, shares, 5000, mel(2026, time.November, 7, 18))
				seedIncome(t, ctx, c, "Dividend", 100, recurringincome.CadenceMonthly, utcDay(2026, time.November, 15), incomeOn(shares))
				card := seedAccount(t, ctx, c, "Card", account.ClassLiability, account.TypeCreditCard)
				seedSnapshot(t, ctx, c, card, -200, mel(2026, time.November, 8, 9))
				seedBill(t, ctx, c, "Insurance", 50, recurringbill.CadenceMonthly, utcDay(2026, time.November, 20), onAccount(card))
			},
			wantEvents:   []string{"2026-11-20 bill Insurance -50"},
			wantEnd:      map[string]float64{"Everyday": 1000, "Shares": 5000, "Card": -250, "": 5750},
			wantExcluded: []string{"income Dividend investment_account"},
			check: func(t *testing.T, v ProjectionView) {
				shares := seriesByName(t, v, "Shares")
				if !shares.Flat {
					t.Error("investment series not marked flat")
				}
				for _, p := range shares.Points {
					if p.Balance != 5000 || p.In != 0 || p.Out != 0 {
						t.Fatalf("investment point %v = %v (in %v out %v), want flat 5000", p.AsOf, p.Balance, p.In, p.Out)
					}
				}
			},
		},
		{
			name:    "a declaration on an unread account or in another currency is reported",
			horizon: 28,
			seed: func(t *testing.T, ctx context.Context, c *ent.Client, f projFixture) {
				unread := seedAccount(t, ctx, c, "Unread", account.ClassAsset, account.TypeEveryday)
				seedBill(t, ctx, c, "On unread", 10, recurringbill.CadenceWeekly, utcDay(2026, time.November, 10), onAccount(unread))
				seedBill(t, ctx, c, "Foreign", 10, recurringbill.CadenceWeekly, utcDay(2026, time.November, 10), onAccount(f.everyday),
					func(cr *ent.RecurringBillCreate) { cr.SetCurrency("USD") })
				seedIncome(t, ctx, c, "Foreign pay", 10, recurringincome.CadenceWeekly, utcDay(2026, time.November, 10),
					func(cr *ent.RecurringIncomeCreate) { cr.SetCurrency("USD") })
			},
			wantEnd: map[string]float64{"Everyday": 1000, "": 1000},
			wantExcluded: []string{
				"bill Foreign currency_mismatch",
				"bill On unread no_snapshot",
				"income Foreign pay currency_mismatch",
			},
			check: func(t *testing.T, v ProjectionView) {
				unread := seriesByName(t, v, "Unread")
				if len(unread.Points) != 0 || unread.Start != nil || unread.Note == "" {
					t.Errorf("unread account = %d points, start %v, note %q; want an empty line that says why", len(unread.Points), unread.Start, unread.Note)
				}
			},
		},
		{
			name:     "a wanted wishlist item with a cost and a deadline lands on its deadline when asked for",
			horizon:  28,
			wishlist: true,
			seed:     seedWishlistFixture,
			wantEvents: []string{
				"2026-11-20 wishlist Glasses -300",
			},
			wantEnd: map[string]float64{"Everyday": 1000, "": 700},
			// Due before the reading, or on its day, is never applied, and is said so.
			wantExcluded: []string{
				"wishlist Due today past_deadline",
				"wishlist Overdue want past_deadline",
			},
		},
		{
			name:    "the wishlist is left out unless asked for",
			horizon: 28,
			seed:    seedWishlistFixture,
			wantEnd: map[string]float64{"Everyday": 1000, "": 1000},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := newFinanceTestClient(t)
			everyday := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
			seedSnapshot(t, ctx, client, everyday, 1000, mel(2026, time.November, 8, 9))
			tc.seed(t, ctx, client, projFixture{everyday: everyday})

			v, err := Projection(ctx, client, ProjectionFilter{
				HorizonDays:     tc.horizon,
				Step:            tc.step,
				IncludeWishlist: tc.wishlist,
				Now:             projNow,
			})
			if err != nil {
				t.Fatalf("Projection: %v", err)
			}

			if got := eventStrings(v.Events); !slices.Equal(got, tc.wantEvents) {
				t.Errorf("events =\n  %v\nwant\n  %v", got, tc.wantEvents)
			}
			for name, want := range tc.wantEnd {
				if got := lastClose(t, seriesByName(t, v, name)); !near(got, want) {
					t.Errorf("%q ends at %v, want %v", name, got, want)
				}
			}
			if got := exclusionStrings(v.Excluded); !slices.Equal(got, tc.wantExcluded) {
				t.Errorf("excluded = %v, want %v", got, tc.wantExcluded)
			}
			if tc.check != nil {
				tc.check(t, v)
			}
		})
	}
}

// seedWishlistFixture seeds one item the projection takes (wanted, priced, dated inside the
// horizon) beside one of each kind it must not take: two of those are wanted and priced but
// due on or before the reading's day, which the projection reports as past their deadline.
func seedWishlistFixture(t *testing.T, ctx context.Context, c *ent.Client, _ projFixture) {
	t.Helper()
	item := func(name string, amount *float64, deadline *time.Time, status wishlistitem.Status) {
		c.WishlistItem.Create().SetName(name).SetNillableAmount(amount).SetNillableDeadline(deadline).SetStatus(status).SaveX(ctx)
	}
	cost := func(f float64) *float64 { return &f }
	date := func(y int, m time.Month, d int) *time.Time { u := utcDay(y, m, d); return &u }
	item("Glasses", cost(300), date(2026, time.November, 20), wishlistitem.StatusWanted)
	item("Laptop", cost(2000), nil, wishlistitem.StatusWanted)
	item("Bag", nil, date(2026, time.November, 21), wishlistitem.StatusWanted)
	item("Bike", cost(900), date(2026, time.November, 22), wishlistitem.StatusBought)
	item("Chair", cost(250), date(2027, time.June, 1), wishlistitem.StatusWanted)
	item("Overdue want", cost(80), date(2026, time.November, 1), wishlistitem.StatusWanted)
	item("Due today", cost(50), date(2026, time.November, 8), wishlistitem.StatusWanted)
}

// TestProjection_MelbourneDayWithPinnedClock pins the clock at 01:00 AEDT on the 8th,
// which is still the 7th in UTC. Today, the horizon end and the snapshot's day must all be
// read on the Melbourne calendar: a UTC reading would end the horizon a day early (dropping
// the cycle on the 15th) and file the 00:30 snapshot under the 7th (applying the cycle on
// the 8th that the reading already carries).
func TestProjection_MelbourneDayWithPinnedClock(t *testing.T) {
	ctx := context.Background()
	client := newFinanceTestClient(t)
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	seedSnapshot(t, ctx, client, acc, 100, time.Date(2026, time.November, 7, 13, 30, 0, 0, time.UTC))
	seedBill(t, ctx, client, "Weekly", 10, recurringbill.CadenceWeekly, utcDay(2026, time.November, 1), onAccount(acc))

	v, err := Projection(ctx, client, ProjectionFilter{
		HorizonDays: 7,
		Now:         time.Date(2026, time.November, 7, 14, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Projection: %v", err)
	}
	if !v.Today.Equal(utcDay(2026, time.November, 8)) || !v.End.Equal(utcDay(2026, time.November, 15)) {
		t.Fatalf("today/end = %v / %v, want 8 Nov / 15 Nov", v.Today, v.End)
	}
	if got, want := eventStrings(v.Events), []string{"2026-11-15 bill Weekly -10"}; !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	s := seriesByName(t, v, "Everyday")
	if !s.Points[0].AsOf.Equal(mel(2026, time.November, 8, 0)) || len(s.Points) != 8 {
		t.Errorf("series starts %v with %d points, want 8 Nov with 8", s.Points[0].AsOf, len(s.Points))
	}
	if got := lastClose(t, s); got != 90 {
		t.Errorf("closes at %v, want 90", got)
	}
}

// TestProjection_NoSnapshots: with no reading anywhere there is no level to start from, so
// every line is empty and net worth says why, rather than projecting from zero.
func TestProjection_NoSnapshots(t *testing.T) {
	ctx := context.Background()
	client := newFinanceTestClient(t)
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	seedBill(t, ctx, client, "Rent", 100, recurringbill.CadenceWeekly, utcDay(2026, time.November, 10), onAccount(acc))

	v, err := Projection(ctx, client, ProjectionFilter{Now: projNow})
	if err != nil {
		t.Fatalf("Projection: %v", err)
	}
	if len(v.NetWorth.Points) != 0 || v.NetWorth.Note == "" || v.NetWorth.Start != nil {
		t.Errorf("net worth = %d points, note %q, start %v; want empty with a reason", len(v.NetWorth.Points), v.NetWorth.Note, v.NetWorth.Start)
	}
	if len(v.Accounts) != 1 || len(v.Accounts[0].Points) != 0 {
		t.Errorf("accounts = %+v, want one empty line", v.Accounts)
	}
	if len(v.Events) != 0 {
		t.Errorf("events = %v, want none", eventStrings(v.Events))
	}
}

// TestProjection_HorizonBounds: an unset horizon is the default and an oversized one is
// clamped, so the walk is always bounded.
func TestProjection_HorizonBounds(t *testing.T) {
	ctx := context.Background()
	client := newFinanceTestClient(t)
	for _, tc := range []struct{ in, want int }{
		{0, DefaultProjectionHorizonDays},
		{-5, DefaultProjectionHorizonDays},
		{MaxProjectionHorizonDays + 1, MaxProjectionHorizonDays},
		{30, 30},
	} {
		v, err := Projection(ctx, client, ProjectionFilter{HorizonDays: tc.in, Now: projNow})
		if err != nil {
			t.Fatalf("Projection(%d): %v", tc.in, err)
		}
		if v.HorizonDays != tc.want || !v.End.Equal(v.Today.AddDate(0, 0, tc.want)) {
			t.Errorf("horizon %d -> %d ending %v, want %d", tc.in, v.HorizonDays, v.End, tc.want)
		}
	}
}
