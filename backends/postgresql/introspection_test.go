package postgresql

import (
	"database/sql"
	"slices"
	"testing"
)

func TestDecodeStrings(t *testing.T) {
	cases := []struct {
		name    string
		in      sql.NullString
		want    []string
		wantErr bool
	}{
		{"null", sql.NullString{}, nil, false},
		{"empty", sql.NullString{String: "", Valid: true}, nil, false},
		{"columns", sql.NullString{String: `["a","b"]`, Valid: true}, []string{"a", "b"}, false},
		{"nulls dropped", sql.NullString{String: `["a",null]`, Valid: true}, []string{"a"}, false},
		// A value that is not a json array of strings must be an error: a
		// constraint reported with no columns at all would make the schema
		// editor drop the wrong object, or none.
		{"not json", sql.NullString{String: "{oops", Valid: true}, nil, true},
		{"wrong shape", sql.NullString{String: `{"a":1}`, Valid: true}, nil, true},
	}
	for _, c := range cases {
		got, err := decodeStrings(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: decodeStrings(%q) error = %v, wantErr %v", c.name, c.in.String, err, c.wantErr)
			continue
		}
		if err == nil && !slices.Equal(got, c.want) {
			t.Errorf("%s: decodeStrings(%q) = %q, want %q", c.name, c.in.String, got, c.want)
		}
	}
}
