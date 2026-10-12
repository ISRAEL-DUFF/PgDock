package messaging

import "testing"

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"08031234567": "+2348031234567", "0803 123 4567": "+2348031234567", "2348031234567": "+2348031234567",
		"+234 803 123 4567": "+2348031234567", "+447911123456": "+447911123456", "00447911123456": "+447911123456",
		"+1 (415) 555-0100": "+14155550100",
	} {
		if got, err := Normalize(in); err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "12345", "+234803123", "+23480312345678", "0803123456", "+0123456789", "080312345678", "+44 7911 123456 x9", "+2348031234567;"} {
		if got, err := Normalize(bad); err == nil {
			t.Errorf("Normalize(%q) = %q, want an error", bad, got)
		}
	}
}

func TestCountry(t *testing.T) {
	for in, want := range map[string]string{"+2348031234567": "NG", "+233241234567": "GH", "+447911123456": "GB",
		"+14155550100": "US", "+254712345678": "KE", "+8613800138000": "CN", "+999123456789": ""} {
		if got := Country(in); got != want {
			t.Errorf("Country(%q) = %q, want %q", in, got, want)
		}
	}
	if Mask("+2348031234567") != "+234••••••4567" {
		t.Fatal(Mask("+2348031234567"))
	}
}
