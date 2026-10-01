package provision

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"blog":                         "blog",
		"My Blog!":                     "my_blog",
		"  todo -- app  ":              "todo_app",
		"2048 game":                    "p_2048_game",
		"Café Olé":                     "caf_ol",
		strings.Repeat("a", 80):        strings.Repeat("a", 40),
		"x" + strings.Repeat("_y", 30): "x_y_y_y_y_y_y_y_y_y_y_y_y_y_y_y_y_y_y_y",
	} {
		got, err := Slugify(in)
		if err != nil || got != want {
			t.Errorf("Slugify(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "!!!", "日本"} {
		if _, err := Slugify(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("Slugify(%q) should fail", in)
		}
	}
}

func TestValidateName(t *testing.T) {
	if got, err := ValidateName("  Blog  "); err != nil || got != "Blog" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, in := range []string{"", "   ", strings.Repeat("x", 65), "a\nb"} {
		if _, err := ValidateName(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidateName(%q) should fail", in)
		}
	}
}

var identRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func TestNames(t *testing.T) {
	db, role, err := Names("blog")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^blog_[a-z0-9]{4}$`).MatchString(db) || role != db+"_owner" {
		t.Fatalf("got %q %q", db, role)
	}
	long, _ := Slugify(strings.Repeat("a", 100))
	db, role, _ = Names(long)
	if len(role) > 63 || !identRe.MatchString(db) || !identRe.MatchString(role) {
		t.Fatalf("names too long or invalid: %q %q", db, role)
	}
}

func TestGeneratePassword(t *testing.T) {
	a, _ := GeneratePassword()
	b, _ := GeneratePassword()
	if a == b || len(a) != 43 || !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(a) {
		t.Fatalf("bad passwords %q %q", a, b)
	}
}
