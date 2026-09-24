package gormgate_test

// This file is the source of the Go examples in the documentation: the
// guide pages include the sections below rather than repeating them, so an
// example that stops compiling stops the build instead of quietly going
// stale. The markers are read by mkdocs' snippets extension.
//
// Add an example here first, then include it; do not paste code into a
// page.

import (
	"context"
	"fmt"
	"testing"

	"gorm.io/gorm"

	"github.com/ctolon/gormgate"
	m "github.com/ctolon/gormgate/migrations"
	pg "github.com/ctolon/gormgate/migrations/postgres"
)

// --8<-- [start:meta]
func (Article) MigrationMeta() gormgate.Meta {
	return gormgate.Meta{
		UniqueTogether: [][]string{{"author_id", "slug"}},
		Indexes: []gormgate.Index{
			{Name: "idx_article_recent", Fields: []gormgate.IndexField{
				{Column: "created_at", Sort: m.SortDesc},
			}},
		},
		Constraints: []gormgate.Constraint{
			&gormgate.CheckConstraint{Name: "chk_article_views", Check: "views >= 0"},
		},
		DBTableComment: "articles, newest first",
	}
}

// --8<-- [end:meta]

// --8<-- [start:unmanaged]
func (LegacyAudit) MigrationMeta() gormgate.Meta {
	return gormgate.Meta{Managed: gormgate.Ptr(false)}
}

// --8<-- [end:unmanaged]

// --8<-- [start:hook]
func announce(r gormgate.MigrateRun) error {
	if r.Verbosity >= 1 {
		fmt.Printf("%s: %d migration(s) on %s\n", r.App, len(r.Plan), r.Conn.Alias())
	}
	return nil
}

// --8<-- [end:hook]

// --8<-- [start:datamigration]
func backfillSlugs(apps *m.Apps, ed m.SchemaEditor) error {
	article, err := apps.GetModel("blog", "article")
	if err != nil {
		return err
	}
	rows, err := article.Objects(ed).Find(map[string]any{"slug": ""})
	if err != nil {
		return err
	}
	for _, row := range rows {
		_, err := article.Objects(ed).Update(
			map[string]any{"id": row["id"]},
			map[string]any{"slug": slugify(row["title"].(string))},
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// --8<-- [end:datamigration]

// --8<-- [start:backfilloperations]
var backfillOperations = []m.Operation{
	&m.AddField{ModelName: "article", Name: "slug",
		Field: m.Field{Type: m.String, Size: 200, Null: true}},
	&m.RunGo{Code: backfillSlugs, ReverseCode: m.RunGoNoop},
	&m.AlterField{ModelName: "article", Name: "slug",
		Field: m.Field{Type: m.String, Size: 200}},
}

// --8<-- [end:backfilloperations]

// --8<-- [start:settings]
func blogSettings() *gormgate.Settings {
	return &gormgate.Settings{
		Apps: []*gormgate.AppConfig{
			gormgate.App("blog", &Article{}),
		},
		Databases: map[string]gormgate.Database{
			"default": {Open: func(ctx context.Context) (*gorm.DB, error) {
				return gorm.Open(yourDriver("the DSN"))
			}},
			"reports": {Open: func(ctx context.Context) (*gorm.DB, error) {
				return gorm.Open(yourDriver("the reporting DSN"))
			}},
		},
		Routers:     []gormgate.Router{reportsRouter{}},
		PostMigrate: map[string][]gormgate.Hook{"blog": {announce}},
	}
}

// --8<-- [end:settings]

// --8<-- [start:abstractbase]
// Timestamps is the counterpart of a Django abstract base class: its
// fields are part of every table that embeds it, and migrations treat them
// like any others.
type Timestamps struct {
	CreatedAt int64
	UpdatedAt int64
}

type Comment struct {
	ID   uint   `gorm:"primaryKey"`
	Body string `gorm:"size:500;not null"`
	Timestamps
}

// --8<-- [end:abstractbase]

// --8<-- [start:mti]
// Django's multi-table inheritance is written out: the child has its own
// table and its own key to the parent, which is what Django generates
// implicitly.
type Place struct {
	ID      uint   `gorm:"primaryKey"`
	Name    string `gorm:"size:100;not null"`
	Address string `gorm:"size:200"`
}

type Restaurant struct {
	PlaceID     uint  `gorm:"primaryKey"`
	Place       Place `gorm:"constraint:OnDelete:CASCADE"`
	ServesPizza bool
}

// --8<-- [end:mti]

// --8<-- [start:polymorphic]
// gorm's polymorphic association is the counterpart of a
// GenericForeignKey: gormgate migrates the id and type columns, without a
// foreign key, because the target is not one table.
type Attachment struct {
	ID        uint   `gorm:"primaryKey"`
	OwnerID   uint   `gorm:"index"`
	OwnerType string `gorm:"size:50;index"`
	Filename  string `gorm:"size:200;not null"`
}

type Ticket struct {
	ID          uint         `gorm:"primaryKey"`
	Attachments []Attachment `gorm:"polymorphic:Owner"`
}

// --8<-- [end:polymorphic]

// --8<-- [start:ordering]
// order_with_respect_to, written out: one column you own, plus the
// uniqueness Django adds with it.
type Chapter struct {
	ID       uint `gorm:"primaryKey"`
	BookID   uint `gorm:"not null;index"`
	Position int  `gorm:"not null;default:0"`
}

func (Chapter) MigrationMeta() gormgate.Meta {
	return gormgate.Meta{UniqueTogether: [][]string{{"book_id", "position"}}}
}

// --8<-- [end:ordering]

// --8<-- [start:compositefk]
// A foreign key over one column lives on the field it constrains. A key
// over several cannot, so gorm's foreignKey:A,B;references:X,Y becomes a
// table-level constraint.
type Barn struct {
	TenantID uint   `gorm:"primaryKey"`
	Code     string `gorm:"primaryKey;size:20"`
}

type Stall struct {
	ID           uint `gorm:"primaryKey"`
	BarnTenantID uint
	BarnCode     string `gorm:"size:20"`
	Barn         Barn   `gorm:"foreignKey:BarnTenantID,BarnCode;references:TenantID,Code"`
}

// --8<-- [end:compositefk]

// --8<-- [start:compositefkop]
// What makemigrations writes for it, under gorm's own constraint name.
var stallForeignKey = &m.AddConstraint{
	ModelName: "Stall",
	Constraint: &m.ForeignKeyConstraint{
		Name:     "fk_stalls_barn",
		Fields:   []string{"barn_tenant_id", "barn_code"},
		To:       "barnyard.Barn",
		ToFields: []string{"tenant_id", "code"},
	},
}

// --8<-- [end:compositefkop]

// --8<-- [start:pgoperations]
// PostgreSQL's own operations. The migration must be non-atomic: creating
// an index concurrently cannot run inside a transaction, and the operation
// says so if you forget.
var trigramMigration = &m.Migration{
	App:    "blog",
	Name:   "0002_trigram",
	Atomic: m.Ptr(false),
	Operations: []m.Operation{
		pg.TrigramExtension(),
		&pg.AddIndexConcurrently{
			ModelName: "article",
			Index: m.Index{
				Name:   "idx_article_title",
				Type:   "gin",
				Fields: m.Columns("title"),
			},
		},
	},
}

// --8<-- [end:pgoperations]

// --8<-- [start:scope]
// A gorm scope is the query-layer counterpart of a Django manager: it
// narrows a query, and nothing about it reaches the schema, so no
// migration records it.
func published(db *gorm.DB) *gorm.DB {
	return db.Where("published_at IS NOT NULL")
}

// --8<-- [end:scope]

// --8<-- [start:swappable]
// The counterpart of AUTH_USER_MODEL: the choice is made where Settings is
// built, in ordinary Go, instead of being resolved from a setting at
// import time.
func settingsFor(userModel any) *gormgate.Settings {
	return &gormgate.Settings{
		Apps: []*gormgate.AppConfig{
			gormgate.App("auth", userModel),
			gormgate.App("blog", &Article{}),
		},
	}
}

// --8<-- [end:swappable]

// Article and LegacyAudit are the models the examples above declare options
// for; slugify stands in for whatever the data migration computes.
type Article struct {
	ID        uint   `gorm:"primaryKey"`
	Title     string `gorm:"size:200;not null"`
	Slug      string `gorm:"size:200"`
	AuthorID  uint   `gorm:"not null"`
	Views     int    `gorm:"not null;default:0"`
	CreatedAt int64
}

type LegacyAudit struct {
	ID uint `gorm:"primaryKey"`
}

func slugify(title string) string { return title }

// TestDocSnippets exercises the documented code, so that the guide cannot
// show an example that does not work. Compiling is most of the value here;
// what can be asserted without a database is asserted.
func TestDocSnippets(t *testing.T) {
	meta := (Article{}).MigrationMeta()
	if got := meta.Constraints[0].ConstraintName(); got != "chk_article_views" {
		t.Errorf("constraint name = %q", got)
	}
	managed := (LegacyAudit{}).MigrationMeta().Managed
	if managed == nil || *managed {
		t.Error("the unmanaged example must set Managed to false")
	}
	s := blogSettings()
	if _, ok := s.Databases["reports"]; !ok {
		t.Error("the settings example must declare the reports database")
	}
	if len(s.PostMigrate["blog"]) != 1 {
		t.Error("the settings example must register the post-migrate hook")
	}
	// The backfill order is the point of that example: add the column,
	// fill it, then make it NOT NULL.
	if len(backfillOperations) != 3 {
		t.Fatalf("backfill has %d operations, want 3", len(backfillOperations))
	}
	if _, ok := backfillOperations[1].(*m.RunGo); !ok {
		t.Errorf("the data migration must sit between the two field operations, got %T", backfillOperations[1])
	}
	// The PostgreSQL example must be non-atomic, which is the whole point
	// of the concurrent index it contains.
	if trigramMigration.Atomic == nil || *trigramMigration.Atomic {
		t.Error("the trigram example must set Atomic to false")
	}
	// The scope is a gorm.Scopes argument; that it has the right shape is
	// the whole claim, and checking it needs no database.
	var _ func(*gorm.DB) *gorm.DB = published
	fk, ok := stallForeignKey.Constraint.(*m.ForeignKeyConstraint)
	if !ok {
		t.Fatalf("the composite key example is a %T", stallForeignKey.Constraint)
	}
	if len(fk.Fields) != len(fk.ToFields) {
		t.Errorf("a composite key needs the same number of columns on both sides, got %d and %d",
			len(fk.Fields), len(fk.ToFields))
	}
	if apps := settingsFor(&Article{}).Apps; len(apps) != 2 {
		t.Errorf("the swappable example declared %d apps, want 2", len(apps))
	}
}
