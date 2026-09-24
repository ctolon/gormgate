package base

import (
	"maps"
	"slices"
	"strings"
	"testing"

	m "github.com/ctolon/gormgate/migrations"
)

// The three tests that used to live here pinned Format's behaviour: that
// it panicked on a missing %(name)s part, that it tolerated an extra one,
// and that Statement.String did the same. There is nothing left to pin: a
// statement is spelled by a Grammar method taking a struct, so a part that
// is not supplied is an empty field rather than a missing key, and one the
// backend does not use is simply ignored.

func TestFirstPrimaryKeyIsDeterministic(t *testing.T) {
	// The constraints of a table are a map, and Go randomizes the order a
	// map is ranged in: with more than one entry marked as a primary key
	// the answer still has to be the same on every call.
	cons := map[string]ConstraintInfo{
		"b_pk":  {Columns: []string{"b"}, PrimaryKey: true},
		"a_pk":  {Columns: []string{"a"}, PrimaryKey: true},
		"c_idx": {Columns: []string{"c"}, Index: true},
	}
	for range 50 {
		if got := FirstPrimaryKey(cons); !slices.Equal(got, []string{"a"}) {
			t.Fatalf("FirstPrimaryKey = %q, want [a]", got)
		}
	}
	if got := FirstPrimaryKey(maps.Clone(map[string]ConstraintInfo{})); got != nil {
		t.Fatalf("FirstPrimaryKey of no constraints = %q, want nil", got)
	}
}

func TestParseSortOrder(t *testing.T) {
	for _, c := range []struct {
		in   string
		want m.SortOrder
	}{
		{"", ""},
		{"ASC", m.SortAsc},
		{"DESC", m.SortDesc},
		{"desc", m.SortDesc},
		{" ASC ", m.SortAsc},
	} {
		got, err := ParseSortOrder(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseSortOrder(%q) = %q, %v, want %q, nil", c.in, got, err, c.want)
		}
	}
	// An unexpected spelling is reported rather than folded into ascending.
	got, err := ParseSortOrder("NULLS FIRST")
	if err == nil {
		t.Fatalf("ParseSortOrder of an unknown order = %q, want an error", got)
	}
	if !strings.Contains(err.Error(), `"NULLS FIRST"`) {
		t.Errorf("error %q does not name the order", err)
	}
	if _, err := ParseSortOrders([]string{"ASC", "sideways"}); err == nil {
		t.Error("ParseSortOrders of an unknown order did not fail")
	}
	if got, err := ParseSortOrders(nil); got != nil || err != nil {
		t.Errorf("ParseSortOrders(nil) = %v, %v, want nil, nil", got, err)
	}
}
