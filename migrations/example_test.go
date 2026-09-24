package migrations_test

import (
	"fmt"

	m "github.com/ctolon/gormgate/migrations"
)

// A migration is a Go file that registers itself from init(). This is what
// makemigrations writes, and what you edit by hand when you need an
// operation it could not have guessed.
func ExampleRegister() {
	mig := &m.Migration{
		App:          "blog",
		Name:         "0002_post_views",
		Dependencies: []m.Key{{App: "blog", Name: "0001_initial"}},
		Operations: []m.Operation{
			&m.AddField{
				ModelName: "post",
				Name:      "views",
				Field: m.Field{
					Type:      m.Int,
					Size:      64,
					DBDefault: m.DBValue(int64(0)),
				},
			},
		},
	}
	// In a real migration file this is m.Register(mig) inside init().
	for _, op := range mig.Operations {
		fmt.Println(m.FormattedDescription(op))
	}
	// Output:
	// + add field views to post
}

// Columns is shorthand for an index over plain columns.
func ExampleColumns() {
	op := &m.AddIndex{
		ModelName: "post",
		Index: m.Index{
			Name:   "idx_post_author_created",
			Fields: m.Columns("author_id", "created_at"),
		},
	}
	fmt.Println(m.FormattedDescription(op))
	// Output:
	// + create index idx_post_author_created on field(s) author_id, created_at of model post
}

// An index element can be an expression or carry a sort order, which
// Columns does not cover.
func ExampleIndex() {
	ix := m.Index{
		Name: "idx_post_recent",
		Fields: []m.IndexField{
			{Column: "created_at", Sort: "DESC"},
			{Expression: "lower(title)"},
		},
		Where: "published",
	}
	fmt.Println(ix.Name, len(ix.Fields), ix.Where)
	// Output:
	// idx_post_recent 2 published
}

// RunSQL runs statements the operations cannot express. Without a
// ReverseSQL the migration cannot be unapplied; RunSQLNoop says "undoing
// this needs nothing".
func ExampleRunSQL() {
	op := &m.RunSQL{
		SQL:        m.Script("CREATE EXTENSION IF NOT EXISTS pg_trgm"),
		ReverseSQL: m.NoSQL,
	}
	fmt.Println(m.FormattedDescription(op), op.Reversible())
	// Output:
	// s raw SQL operation true
}

// A data migration changes rows. Its function takes the historical models
// -- the models as they were at this point in the history, not today's Go
// structs -- so that it still works when the migration is replayed on a
// fresh database years later.
func ExampleRunGo() {
	backfill := func(apps *m.Apps, ed m.SchemaEditor) error {
		post, err := apps.GetModel("blog", "post")
		if err != nil {
			return err
		}
		rows, err := post.Objects(ed).Find(map[string]any{"slug": ""})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if _, err := post.Objects(ed).Update(
				map[string]any{"id": row["id"]},
				map[string]any{"slug": fmt.Sprint(row["id"])},
			); err != nil {
				return err
			}
		}
		return nil
	}

	// In a migration file, Code must be a package-level function: a
	// closure cannot be written back into generated source.
	op := &m.RunGo{Code: backfill, ReverseCode: m.RunGoNoop}
	fmt.Println(m.FormattedDescription(op), op.Reversible())
	// Output:
	// p raw Go operation true
}

// MigrationMeta declares what gorm tags cannot: multi-column constraints,
// named indexes with options, and a table comment.
func ExampleMeta() {
	meta := m.Meta{
		UniqueTogether: [][]string{{"author_id", "slug"}},
		Constraints: []m.Constraint{
			&m.CheckConstraint{Name: "chk_post_views", Check: "views >= 0"},
		},
		DBTableComment: "posts, newest first",
	}
	fmt.Println(meta.UniqueTogether, meta.Constraints[0].ConstraintName(), meta.DBTableComment)
	// Output:
	// [[author_id slug]] chk_post_views posts, newest first
}
