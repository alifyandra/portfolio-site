// Package whatsapp holds the WhatsApp Sender tool's server-side logic: phone
// normalization, bulk-paste parsing, and the streaming client that dials the
// private whatsapp-web.js sidecar. See ADR 11 and docs/whatsapp-sidecar-contract.md.
package whatsapp

import (
	"strings"
)

// DefaultCountryCode is prepended when a pasted number starts with a local
// trunk 0. The MVP targets Australia (+61); any non-Australian number must be
// pasted with its own country code. See ADR 11's 2026-06-30 amendment.
const DefaultCountryCode = "61"

// phone length bounds after normalization. E.164 caps a number at 15 digits;
// 8 is a floor that rejects obvious junk while allowing short national numbers.
const (
	minPhoneDigits = 8
	maxPhoneDigits = 15
)

// NormalizePhone canonicalizes a pasted number to international, digits-only form
// (no +, no leading 0). It strips whitespace and punctuation, replaces a single
// leading 0 with DefaultCountryCode, and validates the digit count. ok is false
// when nothing usable remains or the result is out of range.
func NormalizePhone(raw string) (normalized string, ok bool) {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if digits == "" {
		return "", false
	}
	// A leading trunk 0 means a local number: swap it for the default country code.
	if strings.HasPrefix(digits, "0") {
		digits = DefaultCountryCode + digits[1:]
	}
	if len(digits) < minPhoneDigits || len(digits) > maxPhoneDigits {
		return "", false
	}
	return digits, true
}

// ParsedRecipient is one valid entry produced by ParseRecipients.
type ParsedRecipient struct {
	Phone string
	Name  string
}

// LineError reports a bulk-paste line that could not be parsed into a recipient,
// so the caller can show which lines to fix without discarding the whole paste.
type LineError struct {
	Line   int    `json:"line"`
	Raw    string `json:"raw"`
	Reason string `json:"reason"`
}

// ParseRecipients turns bulk-paste text into recipients plus per-line errors.
// Each non-blank line is "phone" or "phone<sep>name", where sep is the first
// comma or tab; the phone is normalized and the name trimmed. Blank lines are
// ignored. Duplicate numbers (after normalization) are dropped, keeping the
// first occurrence, so a number is never messaged twice in one list.
func ParseRecipients(text string) (recipients []ParsedRecipient, errs []LineError) {
	seen := make(map[string]struct{})
	for i, line := range strings.Split(text, "\n") {
		raw := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if raw == "" {
			continue
		}
		phonePart, namePart := splitPhoneName(raw)
		phone, ok := NormalizePhone(phonePart)
		if !ok {
			errs = append(errs, LineError{Line: i + 1, Raw: raw, Reason: "not a valid phone number"})
			continue
		}
		if _, dup := seen[phone]; dup {
			continue
		}
		seen[phone] = struct{}{}
		recipients = append(recipients, ParsedRecipient{Phone: phone, Name: strings.TrimSpace(namePart)})
	}
	return recipients, errs
}

// splitPhoneName splits a line into its phone token and an optional name on the
// first comma or tab. With no separator the whole line is the phone token.
func splitPhoneName(line string) (phone, name string) {
	if idx := strings.IndexAny(line, ",\t"); idx >= 0 {
		return line[:idx], line[idx+1:]
	}
	return line, ""
}
