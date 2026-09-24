package mssql

import (
	"testing"

	m "github.com/ctolon/gormgate/migrations"
)

func TestQuoteName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"t", `"t"`},
		{`"t"`, `"t"`},
		// gorm's sqlserver dialector splits on dots, so a name carrying one
		// is quoted as a qualified name; gormgate matches it.
		{"dbo.t", `"dbo"."t"`},
		// A quote inside a part is doubled, as it is on every other
		// backend: a column named a"b must not end its own quoting.
		{`a"b`, `"a""b"`},
		{`dbo.a"b`, `"dbo"."a""b"`},
	}
	for _, c := range cases {
		if got := (Ops{}).QuoteName(c.in); got != c.want {
			t.Errorf("QuoteName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestReferentialAction(t *testing.T) {
	for _, c := range []struct {
		desc string
		want m.ReferentialAction
	}{
		{"NO_ACTION", ""},
		{"CASCADE", m.Cascade},
		{"SET_NULL", m.SetNull},
		{"SET_DEFAULT", m.SetDefault},
	} {
		got, err := referentialAction(c.desc)
		if err != nil || got != c.want {
			t.Errorf("referentialAction(%q) = %q, %v, want %q, nil", c.desc, got, err, c.want)
		}
	}
	// A description SQL Server does not document is reported rather than
	// silently turned into "no clause".
	if got, err := referentialAction("RESTRICT"); err == nil {
		t.Errorf("referentialAction of an unknown action = %q, want an error", got)
	}
}
