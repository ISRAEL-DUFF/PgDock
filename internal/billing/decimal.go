package billing

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

// Dec is an exact decimal: unit prices in kobo ("34.25" kobo per GB-hour),
// quantities from usage_records, rates ("0.075"). JSON carries it as a
// string so nothing passes through a float.
type Dec struct{ r *big.Rat }

// D parses s, panicking on a malformed literal (for constants).
func D(s string) Dec {
	d, err := ParseDec(s)
	if err != nil {
		panic(err)
	}
	return d
}

// ParseDec parses a decimal such as "12", "0.075" or "-3.5".
func ParseDec(s string) (Dec, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "/eE") {
		return Dec{}, fmt.Errorf("invalid decimal %q", s)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return Dec{}, fmt.Errorf("invalid decimal %q", s)
	}
	return Dec{r}, nil
}

// DecInt is n as a decimal.
func DecInt(n int64) Dec { return Dec{new(big.Rat).SetInt64(n)} }

// DecFromNumeric converts a Postgres numeric exactly.
func DecFromNumeric(n pgtype.Numeric) Dec {
	if !n.Valid || n.Int == nil || n.NaN {
		return Dec{}
	}
	r := new(big.Rat).SetInt(n.Int)
	if n.Exp > 0 {
		r.Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n.Exp)), nil)))
	} else if n.Exp < 0 {
		r.Quo(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-n.Exp)), nil)))
	}
	return Dec{r}
}

func (d Dec) rat() *big.Rat {
	if d.r == nil {
		return new(big.Rat)
	}
	return d.r
}

// Numeric is d for a numeric column, to 9 decimal places.
func (d Dec) Numeric() pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(d.rat().FloatString(9))
	return n
}

func (d Dec) Add(e Dec) Dec { return Dec{new(big.Rat).Add(d.rat(), e.rat())} }
func (d Dec) Sub(e Dec) Dec { return Dec{new(big.Rat).Sub(d.rat(), e.rat())} }
func (d Dec) Mul(e Dec) Dec { return Dec{new(big.Rat).Mul(d.rat(), e.rat())} }

// Quo is d / e; zero when e is zero.
func (d Dec) Quo(e Dec) Dec {
	if e.Sign() == 0 {
		return Dec{}
	}
	return Dec{new(big.Rat).Quo(d.rat(), e.rat())}
}

func (d Dec) Sign() int      { return d.rat().Sign() }
func (d Dec) Cmp(e Dec) int  { return d.rat().Cmp(e.rat()) }
func (d Dec) IsZero() bool   { return d.Sign() == 0 }
func (d Dec) Float() float64 { f, _ := d.rat().Float64(); return f }
func (d Dec) Neg() Dec       { return Dec{new(big.Rat).Neg(d.rat())} }
func (d Dec) Frac(num, den int64) Dec {
	return d.Mul(Dec{big.NewRat(num, den)})
}

// Max is the larger of d and e.
func (d Dec) Max(e Dec) Dec {
	if d.Cmp(e) >= 0 {
		return d
	}
	return e
}

// Round is d rounded to an integer, halves away from zero (kobo amounts).
func (d Dec) Round() int64 {
	r := d.rat()
	num, den := new(big.Int).Abs(r.Num()), r.Denom()
	q, m := new(big.Int).QuoRem(num, den, new(big.Int))
	if m.Mul(m, big.NewInt(2)).Cmp(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if r.Sign() < 0 {
		q.Neg(q)
	}
	return q.Int64()
}

// String is d with up to 6 decimal places, trailing zeros removed.
func (d Dec) String() string {
	s := d.rat().FloatString(6)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		s = "0"
	}
	return s
}

func (d Dec) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *Dec) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		// Accept a bare JSON number too, read exactly from its text.
		var n json.Number
		if err := json.Unmarshal(b, &n); err != nil {
			return fmt.Errorf("decimal: %w", err)
		}
		s = n.String()
	}
	v, err := ParseDec(s)
	if err != nil {
		return err
	}
	*d = v
	return nil
}
