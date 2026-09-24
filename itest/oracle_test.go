//go:build integration

package itest

import (
	"strings"
	"testing"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/itest/internal/dbtest"
	"github.com/ctolon/gormgate/itest/internal/harness"
	m "github.com/ctolon/gormgate/migrations"
)

// TestOracleSequenceReset applies a table with an identity column and runs
// the PL/SQL block sqlsequencereset prints for it: after the reset the
// sequence must be past the largest key, so that an INSERT without a key
// does not collide with a row inserted with an explicit key.
func TestOracleSequenceReset(t *testing.T) {
	skipUnlessVendor(t, "oracle")
	db, _ := dbtest.Open(t, "oracle")
	p := harness.New(t, db, "sq")
	create := &m.CreateModel{Name: "Pony", Table: "sq_pony", Fields: m.Fields{
		{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
		{Name: "name", Field: m.Field{Type: m.String, Size: 20, Null: true}},
	}}
	mig := &m.Migration{App: "sq", Name: "0001_setup", Operations: []m.Operation{create}}
	p.Register(mig)
	p.MustMigrate(mig.Key())
	defer p.MustMigrate(m.Key{App: "sq", Name: ""})

	st, err := mig.MutateState(m.NewProjectState(), true)
	if err != nil {
		t.Fatal(err)
	}
	apps, err := st.Apps()
	if err != nil {
		t.Fatal(err)
	}
	stmts, err := p.Conn.Backend.SequenceResetSQL(p.Conn, base.PlainStyle{}, apps.Models())
	if err != nil {
		t.Fatalf("sequence_reset_sql: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d statements, want 1:\n%s", len(stmts), strings.Join(stmts, "\n"))
	}
	if err := db.Exec(`INSERT INTO "sq_pony" ("id", "name") VALUES (10, 'explicit')`).Error; err != nil {
		t.Fatal(err)
	}
	// The trailing "/" terminates the block for SQL*Plus and is not part of
	// the statement.
	block := strings.TrimSuffix(strings.TrimSpace(stmts[0]), "/")
	if err := db.Exec(block).Error; err != nil {
		t.Fatalf("%v\n%s", err, block)
	}
	if err := db.Exec(`INSERT INTO "sq_pony" ("name") VALUES ('generated')`).Error; err != nil {
		t.Fatalf("insert after the sequence reset: %v", err)
	}
	var max int64
	if err := db.Raw(`SELECT MAX("id") FROM "sq_pony"`).Row().Scan(&max); err != nil {
		t.Fatal(err)
	}
	if max <= 10 {
		t.Fatalf("the generated key is %d, i.e. not past the explicit one", max)
	}
}
