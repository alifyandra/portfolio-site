// Package finance implements the cloud side of the broker->cloud finance ingest
// (portfolio-site#84): the idempotency hash, the wire payload, and the persistence
// that lands a scraped window into the Account/Transaction ledger. The wire format
// is defined by the shared ingest contract; this package is its authoritative Go
// implementation.
package finance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// DedupHash computes the immutable-ledger idempotency key for a posted transaction.
// The broker sends none of these hashes; the cloud derives them so a re-scraped
// overlap window upserts the same rows. The canonical string joins five fields
// with '|', and the hash is its lowercase-hex SHA-256:
//
//		account | posted_date | amount(2dp) | description(ws-collapsed) | balance_after(2dp)
//
//	  - account is trimmed.
//	  - posted_date is the RAW payload string (e.g. "2025-07-10"), not the parsed time,
//	    so the hash never depends on how we normalize dates for storage.
//	  - amount and balance_after are formatted to exactly two decimals.
//	  - description is trimmed with internal whitespace collapsed to single spaces.
//	  - balance_after contributes an empty field when null (distinct from a 0.00
//	    balance), so it is modeled as a *float64.
//
// This is a locked contract with a pinned test vector (see hash_test.go); do not
// change the field order, formatting, or separator.
func DedupHash(account, postedDate string, amount float64, description string, balanceAfter *float64) string {
	canonical := strings.Join([]string{
		strings.TrimSpace(account),
		postedDate,
		money2dp(amount),
		collapseWhitespace(description),
		balanceField(balanceAfter),
	}, "|")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// balanceField renders a nullable balance for the hash: an empty string when null,
// otherwise the 2dp form. Null must differ from 0.00, hence the pointer.
func balanceField(v *float64) string {
	if v == nil {
		return ""
	}
	return money2dp(*v)
}

// money2dp formats a monetary amount to exactly two decimals. The "-0.00" guard
// aligns with the broker's JS toFixed, which renders negative zero as "0.00": a
// rounded-to-zero negative amount must hash the same on both sides.
func money2dp(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	if s == "-0.00" {
		return "0.00"
	}
	return s
}

// collapseWhitespace trims a description and collapses every run of internal
// whitespace to a single space, so cosmetic spacing differences never fork the hash.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
