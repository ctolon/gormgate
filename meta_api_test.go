package gormgate_test

// This file imports nothing but gormgate on purpose. The re-exported types
// exist so that a model file can declare its Meta options without importing
// the migrations package, and that promise is only real if a Meta using
// every part of it compiles from this package alone. Twice already a type
// was added to Meta and left out of the re-export block, which made the
// documented example fail to compile.

import (
	"testing"

	"github.com/ctolon/gormgate"
)

type metaModel struct {
	ID       uint `gorm:"primaryKey"`
	AuthorID uint
	Slug     string
	Views    int
}

func (metaModel) MigrationMeta() gormgate.Meta {
	return gormgate.Meta{
		Managed:        gormgate.Ptr(true),
		DBTableComment: "every Meta option, from the gormgate package alone",
		UniqueTogether: [][]string{{"author_id", "slug"}},
		Indexes: []gormgate.Index{
			{Name: "idx_meta_recent", Fields: []gormgate.IndexField{
				{Column: "views", Sort: gormgate.SortDesc},
				{Expression: "lower(slug)"},
			}},
		},
		Constraints: []gormgate.Constraint{
			&gormgate.CheckConstraint{Name: "chk_meta_views", Check: "views >= 0"},
			&gormgate.UniqueConstraint{
				Name:       "uq_meta_author_slug",
				Fields:     []string{"author_id", "slug"},
				Deferrable: gormgate.Deferred,
			},
			&gormgate.ForeignKeyConstraint{
				Name:     "fk_meta_author",
				Fields:   []string{"author_id", "slug"},
				To:       "blog.Author",
				ToFields: []string{"id", "slug"},
				OnDelete: gormgate.Cascade,
			},
		},
		ClickHouse: &gormgate.ClickHouseTable{Engine: "MergeTree()", OrderBy: "(id)"},
	}
}

// TestMetaFromRootPackageAlone is a compile-time guard: if it builds, every
// type and value a Meta needs is reachable without importing migrations.
// The assertions keep the declaration from being optimized into nothing.
func TestMetaFromRootPackageAlone(t *testing.T) {
	var provider gormgate.MetaProvider = metaModel{}
	meta := provider.MigrationMeta()

	if got := meta.Indexes[0].Fields[0].Sort; got != gormgate.SortDesc {
		t.Errorf("Sort = %q, want %q", got, gormgate.SortDesc)
	}
	unique, ok := meta.Constraints[1].(*gormgate.UniqueConstraint)
	if !ok {
		t.Fatalf("second constraint is %T, want *gormgate.UniqueConstraint", meta.Constraints[1])
	}
	if unique.Deferrable != gormgate.Deferred {
		t.Errorf("Deferrable = %q, want %q", unique.Deferrable, gormgate.Deferred)
	}
	fk, ok := meta.Constraints[2].(*gormgate.ForeignKeyConstraint)
	if !ok {
		t.Fatalf("third constraint is %T, want *gormgate.ForeignKeyConstraint", meta.Constraints[2])
	}
	var _ gormgate.ReferentialAction = fk.OnDelete
	if fk.OnDelete != gormgate.Cascade {
		t.Errorf("OnDelete = %q, want %q", fk.OnDelete, gormgate.Cascade)
	}
	if meta.Managed == nil || !*meta.Managed {
		t.Error("Managed did not survive the round trip")
	}
}
