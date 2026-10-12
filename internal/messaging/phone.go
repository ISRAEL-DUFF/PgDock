// Package messaging sends one-time codes by SMS and WhatsApp (V4 §4.6):
// phone numbers normalised to E.164, the country they belong to, and the
// providers PGDock or a project sends through (Termii, the WhatsApp
// Business Platform's Cloud API, Twilio, Africa's Talking).
package messaging

import (
	"errors"
	"sort"
	"strings"
)

// ErrBadPhone is a number that isn't a phone number PGDock can send to.
var ErrBadPhone = errors.New("not a valid phone number: use international format, e.g. +2348031234567")

// Normalize returns raw as E.164 (+<country code><number>). Numbers
// without a country code are Nigerian (V4 §4.1): 08031234567 and
// 2348031234567 both become +2348031234567.
func Normalize(raw string) (string, error) {
	var b strings.Builder
	for i, r := range strings.TrimSpace(raw) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return "", ErrBadPhone
		}
	}
	s := b.String()
	switch {
	case strings.HasPrefix(s, "+"):
	case strings.HasPrefix(s, "00"):
		s = "+" + s[2:]
	case strings.HasPrefix(s, "234") && len(s) == 13:
		s = "+" + s
	case strings.HasPrefix(s, "0") && len(s) == 11:
		s = "+234" + s[1:]
	default:
		return "", ErrBadPhone
	}
	digits := s[1:]
	if len(digits) < 8 || len(digits) > 15 || digits[0] == '0' {
		return "", ErrBadPhone
	}
	if strings.HasPrefix(digits, "234") && len(digits) != 13 {
		return "", ErrBadPhone // Nigerian numbers are +234 and 10 digits
	}
	return s, nil
}

// callingCodes maps country calling codes to ISO 3166 countries: Africa
// in full, and the countries most numbers come from elsewhere. Codes
// shared by several countries (+1, +7, +44) name the largest.
var callingCodes = map[string]string{
	"1": "US", "7": "RU", "20": "EG", "27": "ZA", "30": "GR", "31": "NL", "32": "BE", "33": "FR", "34": "ES", "36": "HU",
	"39": "IT", "40": "RO", "41": "CH", "43": "AT", "44": "GB", "45": "DK", "46": "SE", "47": "NO", "48": "PL", "49": "DE",
	"51": "PE", "52": "MX", "53": "CU", "54": "AR", "55": "BR", "56": "CL", "57": "CO", "58": "VE", "60": "MY", "61": "AU",
	"62": "ID", "63": "PH", "64": "NZ", "65": "SG", "66": "TH", "81": "JP", "82": "KR", "84": "VN", "86": "CN", "90": "TR",
	"91": "IN", "92": "PK", "93": "AF", "94": "LK", "95": "MM", "98": "IR", "211": "SS", "212": "MA", "213": "DZ", "216": "TN",
	"218": "LY", "220": "GM", "221": "SN", "222": "MR", "223": "ML", "224": "GN", "225": "CI", "226": "BF", "227": "NE",
	"228": "TG", "229": "BJ", "230": "MU", "231": "LR", "232": "SL", "233": "GH", "234": "NG", "235": "TD", "236": "CF",
	"237": "CM", "238": "CV", "239": "ST", "240": "GQ", "241": "GA", "242": "CG", "243": "CD", "244": "AO", "245": "GW",
	"248": "SC", "249": "SD", "250": "RW", "251": "ET", "252": "SO", "253": "DJ", "254": "KE", "255": "TZ", "256": "UG",
	"257": "BI", "258": "MZ", "260": "ZM", "261": "MG", "263": "ZW", "264": "NA", "265": "MW", "266": "LS", "267": "BW",
	"268": "SZ", "269": "KM", "290": "SH", "291": "ER", "351": "PT", "352": "LU", "353": "IE", "354": "IS", "355": "AL",
	"356": "MT", "357": "CY", "358": "FI", "359": "BG", "370": "LT", "371": "LV", "372": "EE", "380": "UA", "381": "RS",
	"385": "HR", "386": "SI", "420": "CZ", "421": "SK", "852": "HK", "880": "BD", "886": "TW", "960": "MV", "961": "LB",
	"962": "JO", "963": "SY", "964": "IQ", "965": "KW", "966": "SA", "967": "YE", "968": "OM", "970": "PS", "971": "AE",
	"972": "IL", "973": "BH", "974": "QA", "975": "BT", "976": "MN", "977": "NP",
}

// Country is the ISO country of an E.164 number ("" if unknown).
func Country(e164 string) string {
	d := strings.TrimPrefix(e164, "+")
	for n := 3; n >= 1; n-- {
		if len(d) > n {
			if c, ok := callingCodes[d[:n]]; ok {
				return c
			}
		}
	}
	return ""
}

// Countries are the ISO codes Country knows, sorted.
func Countries() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range callingCodes {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// Mask shows a number's country code and last digits only (+234•••••4567).
func Mask(e164 string) string {
	if len(e164) < 8 {
		return "•••"
	}
	return e164[:4] + strings.Repeat("•", len(e164)-8) + e164[len(e164)-4:]
}
