package console

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestValidateWhere(t *testing.T) {
	ok := []string{
		`email = 'user@email.com' or phone_number like '081%'`,
		`id in (select customer_id from orders where total > 10)`,
		`created_at > now() - interval '1 day'`,
		`"order" = 1`,
		`name = 'it''s; fine -- not a comment'`,
		`tags @> $q$a; b$q$ and x = 1`,
		`exists (select 1 from orders o where o.customer_id = customers.id order by 1 limit 1)`,
		`id = 1 -- trailing comment`,
		`/* note */ id = 2`,
		`substring(name from 1 for 2) = 'Cu'`,
	}
	for _, e := range ok {
		if err := ValidateWhere(e); err != nil {
			t.Errorf("ValidateWhere(%q) = %v, want ok", e, err)
		}
	}
	bad := map[string]string{
		``:                                 "empty",
		`   `:                              "empty",
		`id = 1; drop table x`:             "semicolon",
		`id = 1) union select 1 --`:        ") with no matching",
		`id = 1 union select 1`:            "UNION",
		`id = 1 order by id`:               "ORDER",
		`id = 1 limit 1`:                   "LIMIT",
		`id = 1 for update`:                "FOR",
		`select 1`:                         "SELECT",
		`id in (1, 2`:                      "never closed",
		`name = 'oops`:                     "quote",
		`name = "oops`:                     "quote",
		`/* never ends`:                    "comment",
		`x = $a$ never ends`:               "$-quoted",
		strings.Repeat("a", MaxWhereLen+1): "longer",
	}
	for e, want := range bad {
		err := ValidateWhere(e)
		if err == nil || !errors.Is(err, ErrBadQuery) || !strings.Contains(err.Error(), want) {
			t.Errorf("ValidateWhere(%q) = %v, want ErrBadQuery containing %q", e, err, want)
		}
	}
}

func TestBuildGridWithWhere(t *testing.T) {
	r := relation{kind: "r", pk: []string{"id"}, cols: []string{"id", "email"}}
	g, err := buildGrid(r, "public", "customers", GridQuery{Where: "  email like 'a%'  ", Filters: []Filter{{Column: "id", Op: "gt", Value: ptr("3")}}}, cursor{}, 101)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(g.sql, "WHERE (\nemail like 'a%'\n) AND \"id\" > $1") {
		t.Errorf("sql = %q", g.sql)
	}
	if g.expr != "email like 'a%'" {
		t.Errorf("expr = %q", g.expr)
	}
	if _, err := buildGrid(r, "public", "customers", GridQuery{Where: "1=1; drop table customers"}, cursor{}, 101); !errors.Is(err, ErrBadQuery) {
		t.Errorf("a semicolon: err = %v", err)
	}
	if (GridQuery{Where: "id = 1"}).mode() != modeReadOnly || (GridQuery{}).mode() != modeOwner {
		t.Error("a raw filter must run as the read-only role, and nothing else changes")
	}
}

func TestGridErrorInMovesPositionIntoTheCondition(t *testing.T) {
	expr := "emial = 'x'"
	sql := `SELECT * FROM "public"."customers" WHERE ` + wrapWhere(expr) + ` ORDER BY "id" LIMIT 101`
	start := strings.Index(sql, wrapWhere(expr)) + 2 // 0-based index of the expression
	pe := &pgconn.PgError{Code: "42703", Message: `column "emial" does not exist`, Position: int32(start + 1)}
	err := gridErrorIn(pe, sql, expr)
	var qe *QueryError
	if !errors.As(err, &qe) || qe.Err.Position != 1 {
		t.Fatalf("err = %#v, want a QueryError at position 1", err)
	}
	// An error outside the condition keeps no position.
	pe.Position = 3
	if errors.As(gridErrorIn(pe, sql, expr), &qe); qe.Err.Position != 0 {
		t.Errorf("outside position = %d, want 0", qe.Err.Position)
	}
	// A timeout is the filter's, not the server's.
	if errors.As(gridErrorIn(&pgconn.PgError{Code: "57014", Message: "canceling statement"}, sql, expr), &qe); !strings.Contains(qe.Err.Message, "too long") {
		t.Errorf("timeout: %v", qe.Err)
	}
	// Permission errors stay permission errors.
	if err := gridErrorIn(&pgconn.PgError{Code: "42501", Message: "denied"}, sql, expr); !errors.Is(err, ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

func ptr(s string) *string { return &s }
