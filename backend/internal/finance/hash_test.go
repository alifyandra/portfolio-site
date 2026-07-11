package finance

import (
	"math"
	"testing"
)

func ptr(f float64) *float64 { return &f }

// TestDedupHash_LockedVector asserts the exact digest the shared contract pins
// (ingest-contract.md), so the cloud hash can never silently drift from the broker.
func TestDedupHash_LockedVector(t *testing.T) {
	const want = "57e346aed37e0d17e0b7be83944ed582930fd40b8035725b74270f11886ddc78"
	got := DedupHash("acc1", "2025-07-10", -42.10, "SUPERMARKET", ptr(1957.90))
	if got != want {
		t.Fatalf("DedupHash locked vector = %s, want %s", got, want)
	}
}

// TestDedupHash_WhitespaceCollapse: trimming and internal-whitespace collapse mean
// cosmetically different descriptions hash identically.
func TestDedupHash_WhitespaceCollapse(t *testing.T) {
	a := DedupHash("acc1", "2025-07-10", -42.10, "  EFTPOS   PURCHASE\tSUPERMARKET ", ptr(1957.90))
	b := DedupHash("acc1", "2025-07-10", -42.10, "EFTPOS PURCHASE SUPERMARKET", ptr(1957.90))
	if a != b {
		t.Fatalf("whitespace variants hashed differently:\n  a=%s\n  b=%s", a, b)
	}
}

// TestDedupHash_NilBalanceDiffersFromZero: a null balance_after (empty field) must
// hash differently from an explicit 0.00, so the pointer distinction is load-bearing.
func TestDedupHash_NilBalanceDiffersFromZero(t *testing.T) {
	nilBal := DedupHash("acc1", "2025-07-10", -42.10, "SUPERMARKET", nil)
	zeroBal := DedupHash("acc1", "2025-07-10", -42.10, "SUPERMARKET", ptr(0))
	if nilBal == zeroBal {
		t.Fatalf("nil balance hashed the same as 0.00 (%s); they must differ", nilBal)
	}
}

// TestDedupHash_NegativeZeroGuard: a negative-zero amount renders "0.00" (not
// "-0.00") to match the broker's JS toFixed, so both sides agree on the hash.
func TestDedupHash_NegativeZeroGuard(t *testing.T) {
	negZero := DedupHash("acc1", "2025-07-10", math.Copysign(0, -1), "SUPERMARKET", ptr(0))
	posZero := DedupHash("acc1", "2025-07-10", 0, "SUPERMARKET", ptr(0))
	if negZero != posZero {
		t.Fatalf("negative-zero amount hashed differently from 0.00:\n  neg=%s\n  pos=%s", negZero, posZero)
	}
}
