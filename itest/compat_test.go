//go:build integration

package itest

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/ctolon/gormgate"
	"github.com/ctolon/gormgate/itest/internal/dbtest"
)

// compatAuthor and compatPost declare exactly what a backend can refuse: a
// unique column, a plain index and a foreign key.
type compatAuthor struct {
	ID   uint   `gorm:"primaryKey"`
	Name string `gorm:"size:50;uniqueIndex:uq_compat_author_name"`
}

func (compatAuthor) TableName() string { return "compat_authors" }

type compatPost struct {
	ID       uint   `gorm:"primaryKey"`
	Title    string `gorm:"size:100;index:ix_compat_post_title"`
	AuthorID uint
	Author   compatAuthor `gorm:"foreignKey:AuthorID"`
}

func (compatPost) TableName() string { return "compat_posts" }

// compatCheck runs "check --tag compatibility --database default" against a
// live server and returns what it printed.
func compatCheck(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var out, errOut bytes.Buffer
	settings := &gormgate.Settings{
		Apps: []*gormgate.AppConfig{
			gormgate.App("compat", &compatAuthor{}, &compatPost{}).
				Migrations(t.TempDir(), "example.com/compat/migrations"),
		},
		Databases: map[string]gormgate.Database{
			"default": {Open: func(context.Context) (*gorm.DB, error) { return db, nil }},
		},
		CommandDir: t.TempDir(),
	}
	streams := &gormgate.IO{Stdout: &out, Stderr: &errOut}
	err := gormgate.CallCommand(context.Background(), settings, streams,
		"check", "--tag", "compatibility", "--database", "default")
	report := out.String() + errOut.String()
	if err != nil {
		// A SystemCheckError carries the report; anything else is a bug.
		if !strings.Contains(err.Error(), "System check identified") {
			t.Fatalf("check: %v\n%s", err, report)
		}
		report += err.Error()
	}
	if report == "" {
		t.Fatal("check printed nothing")
	}
	return report
}

// TestCompatibilityChecksClickHouse pins that the compatibility tag reports,
// before anything is migrated, everything ClickHouse will refuse: it has no
// foreign keys, no unique constraints or unique indexes, and no index
// without an explicit type.
func TestCompatibilityChecksClickHouse(t *testing.T) {
	skipUnlessVendor(t, "clickhouse")
	db, _ := chOpen(t)
	report := compatCheck(t, db)
	for _, want := range []string{
		"ERRORS:",
		"(gormgate.E003)",
		"database 'default' (ClickHouse) refuses this model",
		"ClickHouse has no foreign keys",
		"ClickHouse has no unique indexes",
		`only has indexes of an explicit type, but index "ix_compat_post_title" sets no Index.Type`,
		// The inventory is information, and the tag asks for it.
		"(gormgate.I001)",
		"gormgate_migrations",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the ClickHouse report does not contain %q:\n%s", want, report)
		}
	}
}

// TestCompatibilityChecksPostgreSQL is the other side of the same models:
// PostgreSQL refuses none of them, so the tag reports nothing but the
// inventory.
func TestCompatibilityChecksPostgreSQL(t *testing.T) {
	skipUnlessVendor(t, "pg18")
	db, _ := dbtest.Open(t, "pg18")
	report := compatCheck(t, db)
	for _, unwanted := range []string{"ERRORS:", "WARNINGS:", "gormgate.E002", "gormgate.E003", "gormgate.W001", "gormgate.W002"} {
		if strings.Contains(report, unwanted) {
			t.Errorf("the PostgreSQL report contains %q:\n%s", unwanted, report)
		}
	}
	for _, want := range []string{"INFOS:", "(gormgate.I001)", "gormgate_migrations"} {
		if !strings.Contains(report, want) {
			t.Errorf("the PostgreSQL report does not contain %q:\n%s", want, report)
		}
	}
	// The inventory is the only thing there is to say.
	if !strings.Contains(report, "System check identified 1 issue (0 silenced).") {
		t.Errorf("the footer counts more than the inventory:\n%s", report)
	}
}

// TestCompatibilityChecksNamingStrategyDrift pins the worst failure mode the
// tag exists for: gormgate configured with a naming strategy that is not the
// one the application's gorm handle uses.
func TestCompatibilityChecksNamingStrategyDrift(t *testing.T) {
	skipUnlessVendor(t, "pg18")
	db, _ := dbtest.Open(t, "pg18")
	var out, errOut bytes.Buffer
	settings := &gormgate.Settings{
		Apps: []*gormgate.AppConfig{
			gormgate.App("compat", &compatAuthor{}).Migrations(t.TempDir(), "example.com/compat/migrations"),
		},
		Databases: map[string]gormgate.Database{
			"default": {Open: func(context.Context) (*gorm.DB, error) { return db, nil }},
		},
		// The application's gorm handle uses gorm's default strategy; this
		// one prefixes every table, so gormgate would migrate "app_..."
		// tables the application never queries.
		NamingStrategy: schema.NamingStrategy{TablePrefix: "app_"},
		CommandDir:     t.TempDir(),
	}
	streams := &gormgate.IO{Stdout: &out, Stderr: &errOut}
	err := gormgate.CallCommand(context.Background(), settings, streams,
		"check", "--tag", "compatibility", "--database", "default")
	if err == nil {
		t.Fatalf("a drifting naming strategy passed the checks:\n%s%s", out.String(), errOut.String())
	}
	if !strings.Contains(err.Error(), "gormgate.E002") ||
		!strings.Contains(err.Error(), "Settings.NamingStrategy is not the naming strategy of the gorm handle of database 'default'") {
		t.Errorf("error = %v", err)
	}
}
