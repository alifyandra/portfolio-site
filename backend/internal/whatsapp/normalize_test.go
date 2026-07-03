package whatsapp

import "testing"

func TestNormalizePhone(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"australian local trunk zero", "0412345678", "61412345678", true},
		{"already international", "61412345678", "61412345678", true},
		{"plus and spaces stripped", "+61 412 345 678", "61412345678", true},
		{"punctuation stripped", "(04) 1234-5678", "61412345678", true},
		{"foreign with country code", "6281234567890", "6281234567890", true},
		{"empty", "", "", false},
		{"letters only", "not a number", "", false},
		{"too short after normalize", "12345", "", false},
		{"too long", "1234567890123456", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := NormalizePhone(c.in)
			if ok != c.ok || got != c.want {
				t.Errorf("NormalizePhone(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestParseRecipients(t *testing.T) {
	text := "0412345678, Budi\n" + // comma-separated name
		"61498765432\tSiti\n" + // tab-separated name
		"0400000000\n" + // no name
		"\n" + // blank line ignored
		"  0412345678 , Duplicate\n" + // duplicate number dropped
		"garbage line\n" // invalid, reported

	got, errs := ParseRecipients(text)

	want := []ParsedRecipient{
		{Phone: "61412345678", Name: "Budi"},
		{Phone: "61498765432", Name: "Siti"},
		{Phone: "61400000000", Name: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d recipients, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("recipient[%d] = %+v, want %+v", i, got[i], w)
		}
	}
	if len(errs) != 1 {
		t.Fatalf("got %d line errors, want 1: %+v", len(errs), errs)
	}
	if errs[0].Line != 6 || errs[0].Raw != "garbage line" {
		t.Errorf("unexpected line error: %+v", errs[0])
	}
}
