// Common country dialing codes for the WhatsApp Sender (ADR 11). We store and
// send the numeric code only (digits, no leading +); the backend uses it to
// replace a leading trunk 0 on local numbers. Hand-curated and small on
// purpose — the countries Alif and friends actually message, not an ISO dump.

export interface Country {
  /** Dialing code, digits only (no +), exactly as stored/sent to the backend. */
  code: string;
  name: string;
}

export const WA_COUNTRIES: Country[] = [
  { code: '62', name: 'Indonesia' },
  { code: '61', name: 'Australia' },
  { code: '1', name: 'United States' },
  { code: '44', name: 'United Kingdom' },
  { code: '91', name: 'India' },
  { code: '65', name: 'Singapore' },
  { code: '60', name: 'Malaysia' },
  { code: '64', name: 'New Zealand' },
];

/** Country name for a known dialing code, or undefined if unrecognised. */
export function countryName(code: string): string | undefined {
  return WA_COUNTRIES.find((c) => c.code === code)?.name;
}

/** "+62 (Indonesia)" for a known code, or a bare "+62" if unrecognised. */
export function dialLabel(code: string): string {
  const name = countryName(code);
  return name ? `+${code} (${name})` : `+${code}`;
}
