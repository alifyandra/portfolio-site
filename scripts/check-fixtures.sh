#!/usr/bin/env bash
#
# Fail if a masked account number outside the reserved fixture set is committed.
#
# This repo is public, so every committed file is published content: fixtures and doc
# comments included, not just prose. A masked account number copied out of real data
# instead of invented is the easiest kind of leak to miss. It looks like plumbing, it
# gets skimmed in review, and a plausible value raises no flag.
#
# The check is an ALLOWLIST, and that is the whole point. A denylist of the real
# values would have to name them, and this check runs in public CI, so it would
# publish the very thing it protects. Inverted, the check never has to know what the
# real values are, only which fakes are sanctioned.
#
# Adding a value below is therefore a deliberate act that surfaces in review. That
# diff line is the moment to ask whether the new number was invented or copied.
#
# See docs/security.md for the rule this enforces, and for the categories it
# deliberately cannot cover (names, row counts, date spans).

set -euo pipefail

cd "$(dirname "$0")/.."

# Reserved fixture values: repeated-digit, repeated-pair, and the 1234 style used in
# wire-payload tests. All are obviously synthetic at a glance, which is the criterion
# for adding more.
RESERVED='0000 1111 2222 3333 4444 5555 6666 7777 8888 9999 1234 4242 4343 5353 6464'

# A masked number is one or more runs of mask characters followed by a digit run:
# "xxxx 4242", "xxxx xxxx 1234", "****1234", and the inline "xx5353" that CommBank
# transfer descriptions use. Bare four-digit numbers are not in scope, because they
# are ubiquitous (ports, timeouts, IDs) and carry no account semantics on their own.
#
# The digit run is {4,} rather than {4} on purpose. Anchored at exactly four, a mask
# followed by five digits would match only its first four, land on a reserved value
# and pass. A mask followed by more than four digits is never legitimate here, so the
# longer run fails the allowlist and gets looked at.
#
# (Spelling that case out as a literal here would fail this very check: the script is
# a committed file too, and it scans itself.)
SHAPE='[xX*]{2,}[ -]*([xX*]{2,}[ -]*)*[0-9]{4,}'

# git ls-files is the input on purpose: it is exactly the set of committed, published
# files. Generated code (backend/ent, frontend/src/lib/api) is gitignored and so is
# already out of scope. -H forces the filename prefix even when xargs' final batch
# holds a single file; -I skips binaries.
matches=$(git ls-files -z | xargs -0 grep -HnIoE "$SHAPE" 2>/dev/null || true)

violations=''
scanned=0
while IFS= read -r hit; do
	[ -n "$hit" ] || continue
	scanned=$((scanned + 1))
	# Each hit is "file:line:match" and the match ends in the digit run, so stripping
	# everything up to the last non-digit recovers it.
	value=${hit##*[!0-9]}
	case " $RESERVED " in
	*" $value "*) ;;
	*) violations="${violations}${hit}"$'\n' ;;
	esac
done <<EOF
$matches
EOF

if [ -n "$violations" ]; then
	cat >&2 <<'MSG'
error: masked account number outside the reserved fixture set

A masked number is published content in a public repo. Replace each value below with
one from the reserved set in scripts/check-fixtures.sh. If the fixture genuinely needs
a new value, add an obviously-synthetic one to that set in the same commit, so the
choice is visible in review.

MSG
	printf '%s' "$violations" >&2
	exit 1
fi

echo "check-fixtures: ok ($scanned masked number(s), all reserved)"
