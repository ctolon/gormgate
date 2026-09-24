package management

import (
	"errors"
	"testing"

	"github.com/ctolon/gormgate/backends/base"
)

// fakeIntrospection answers each call with the value in the matching field,
// or with the matching error.
type fakeIntrospection struct {
	relations   map[string]base.RelationInfo
	constraints map[string]base.ConstraintInfo
	pk          []string
	columns     []base.ColumnInfo

	relationsErr, constraintsErr, pkErr, columnsErr error
}

func (f fakeIntrospection) TableNames(bool) ([]base.TableInfo, error) { return nil, nil }

func (f fakeIntrospection) TableDescription(string) ([]base.ColumnInfo, error) {
	return f.columns, f.columnsErr
}

func (f fakeIntrospection) Constraints(string) (map[string]base.ConstraintInfo, error) {
	return f.constraints, f.constraintsErr
}

func (f fakeIntrospection) Sequences(string) ([]base.SequenceInfo, error) { return nil, nil }

func (f fakeIntrospection) Relations(string) (map[string]base.RelationInfo, error) {
	return f.relations, f.relationsErr
}

func (f fakeIntrospection) PrimaryKeyColumns(string) ([]string, error) { return f.pk, f.pkErr }

func (f fakeIntrospection) TableComment(string) (string, error) { return "", nil }

func (f fakeIntrospection) IdentifierConverter(name string) string { return name }

// TestDescribeTable checks that every introspection failure aborts the
// table, as Django's outer "except Exception" does. Swallowing one of them
// would produce a model with no primary key, no unique tags or no foreign
// key notes, and inspectdb would still exit 0.
func TestDescribeTable(t *testing.T) {
	boom := errors.New("connection reset")
	for _, tc := range []struct {
		name  string
		intro fakeIntrospection
	}{
		{"Relations fails", fakeIntrospection{relationsErr: boom}},
		{"Constraints fails", fakeIntrospection{constraintsErr: boom}},
		{"PrimaryKeyColumns fails", fakeIntrospection{pkErr: boom}},
		{"TableDescription fails", fakeIntrospection{columnsErr: boom}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, _, err := describeTable(tc.intro, "t")
			if !errors.Is(err, boom) {
				t.Errorf("describeTable = %v, want %v", err, boom)
			}
		})
	}

	t.Run("everything answered", func(t *testing.T) {
		intro := fakeIntrospection{
			relations:   map[string]base.RelationInfo{"author_id": {Column: "author_id", ToColumn: "id", ToTable: "users"}},
			constraints: map[string]base.ConstraintInfo{"u": {Unique: true, Columns: []string{"email"}}},
			pk:          []string{"id"},
			columns:     []base.ColumnInfo{{Name: "id"}, {Name: "email"}},
		}
		rel, cons, pk, cols, err := describeTable(intro, "t")
		if err != nil {
			t.Fatal(err)
		}
		if len(rel) != 1 || len(cons) != 1 || len(pk) != 1 || len(cols) != 2 {
			t.Errorf("describeTable = %v, %v, %v, %v", rel, cons, pk, cols)
		}
	})
}
