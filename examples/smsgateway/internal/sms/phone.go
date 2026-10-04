package sms

import (
	"errors"
	"fmt"
	"strings"
)

// country describes one numbering plan: the ISO code, the dial code, and the
// length range of the national significant number.
type country struct {
	ISO      string
	Dial     string
	Min, Max int
}

// countries is deliberately a table in code rather than a dependency: it is
// the destinations the platform prices and routes. A number whose dial code is
// not listed is rejected as unroutable rather than guessed at. Countries that
// share a dial code (the US and Canada share +1) resolve to the first entry, so
// a deployment that needs to split them routes on a longer prefix.
var countries = []country{
	{"US", "1", 10, 10}, {"RU", "7", 10, 10}, {"EG", "20", 9, 10}, {"ZA", "27", 9, 9},
	{"GR", "30", 10, 10}, {"NL", "31", 9, 9}, {"BE", "32", 8, 9}, {"FR", "33", 9, 9},
	{"ES", "34", 9, 9}, {"IT", "39", 9, 11}, {"RO", "40", 9, 9}, {"CH", "41", 9, 9},
	{"AT", "43", 10, 13}, {"GB", "44", 10, 10}, {"DK", "45", 8, 8}, {"SE", "46", 7, 13},
	{"NO", "47", 8, 8}, {"PL", "48", 9, 9}, {"DE", "49", 10, 11}, {"PE", "51", 9, 9},
	{"MX", "52", 10, 10}, {"AR", "54", 10, 11}, {"BR", "55", 10, 11}, {"CL", "56", 9, 9},
	{"CO", "57", 10, 10}, {"MY", "60", 9, 10}, {"AU", "61", 9, 9}, {"ID", "62", 9, 12},
	{"PH", "63", 10, 10}, {"NZ", "64", 8, 10}, {"SG", "65", 8, 8}, {"TH", "66", 9, 9},
	{"JP", "81", 9, 10}, {"KR", "82", 9, 10}, {"VN", "84", 9, 10}, {"CN", "86", 11, 11},
	{"TR", "90", 10, 10}, {"IN", "91", 10, 10}, {"PK", "92", 10, 10}, {"AF", "93", 9, 9},
	{"LK", "94", 9, 9}, {"MM", "95", 8, 10}, {"IR", "98", 10, 10}, {"NG", "234", 10, 10},
	{"KE", "254", 9, 9}, {"GH", "233", 9, 9}, {"ET", "251", 9, 9}, {"TZ", "255", 9, 9},
	{"UG", "256", 9, 9}, {"PT", "351", 9, 9}, {"IE", "353", 9, 9}, {"FI", "358", 5, 12},
	{"BD", "880", 10, 10}, {"HK", "852", 8, 8}, {"TW", "886", 9, 9}, {"AE", "971", 9, 9},
	{"IL", "972", 9, 9}, {"SA", "966", 9, 9}, {"QA", "974", 8, 8}, {"BT", "975", 8, 8},
	{"NP", "977", 10, 10}, {"UA", "380", 9, 9}, {"CZ", "420", 9, 9},
}

var (
	byDial map[string][]country
	byISO  map[string]country
)

func init() {
	byDial = map[string][]country{}
	byISO = map[string]country{}
	for _, c := range countries {
		byDial[c.Dial] = append(byDial[c.Dial], c)
		byISO[c.ISO] = c
	}
}

// DialCode returns the dial code of an ISO country code.
func DialCode(iso string) (string, bool) {
	c, ok := byISO[strings.ToUpper(iso)]
	return c.Dial, ok
}

// KnownCountry reports whether iso is a destination the platform knows.
func KnownCountry(iso string) bool {
	_, ok := byISO[strings.ToUpper(iso)]
	return ok
}

// ErrNumber is the family of destination-number errors.
var ErrNumber = errors.New("invalid phone number")

// NormalizeNumber converts user input to E.164 digits without the plus sign and
// reports the destination country.
//
// Accepted forms: +9779841234567, 009779841234567, 977 984-123-4567, and a
// national number with a leading 0 or none (9841234567, 09841234567) which is
// read in defaultCountry. Anything else, including letters, is rejected.
func NormalizeNumber(input, defaultCountry string) (digits, iso string, err error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return "", "", fmt.Errorf("%w: empty", ErrNumber)
	}
	international := false
	if strings.HasPrefix(s, "+") {
		international = true
		s = s[1:]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return "", "", fmt.Errorf("%w: unexpected character %q", ErrNumber, r)
		}
	}
	d := b.String()
	if d == "" {
		return "", "", fmt.Errorf("%w: no digits", ErrNumber)
	}
	if !international && strings.HasPrefix(d, "00") {
		international = true
		d = d[2:]
	}
	if !international {
		c, ok := byISO[strings.ToUpper(defaultCountry)]
		if !ok {
			return "", "", fmt.Errorf("%w: %q has no country code and no default country is configured", ErrNumber, input)
		}
		d = strings.TrimLeft(d, "0")
		// A number that already starts with the dial code and has the full
		// length is international without the plus.
		if !(strings.HasPrefix(d, c.Dial) && len(d)-len(c.Dial) >= c.Min && len(d)-len(c.Dial) <= c.Max) {
			d = c.Dial + d
		}
	}
	if len(d) < 8 || len(d) > 15 {
		return "", "", fmt.Errorf("%w: %d digits is not an E.164 length", ErrNumber, len(d))
	}
	iso, err = countryOf(d)
	if err != nil {
		return "", "", err
	}
	return d, iso, nil
}

func countryOf(digits string) (string, error) {
	for l := 3; l >= 1; l-- {
		if len(digits) <= l {
			continue
		}
		cs, ok := byDial[digits[:l]]
		if !ok {
			continue
		}
		nsn := len(digits) - l
		for _, c := range cs {
			if nsn >= c.Min && nsn <= c.Max {
				return c.ISO, nil
			}
		}
		return "", fmt.Errorf("%w: +%s numbers have %d–%d digits after the country code, got %d",
			ErrNumber, cs[0].Dial, cs[0].Min, cs[0].Max, nsn)
	}
	return "", fmt.Errorf("%w: no route to country code of %s", ErrNumber, digits)
}
