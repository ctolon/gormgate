// Package gaussdb is the openGauss / GaussDB backend, used through
// gorm.io/driver/gaussdb.
//
// openGauss forked PostgreSQL 9.2, so it speaks the same dialect and most
// of backends/postgresql applies unchanged; this package overrides what the
// fork lacks (catalog columns and SQL added after 9.2) and what it does
// differently. gormgate requires a database created with
// DBCOMPATIBILITY 'PG': in the default 'A' (Oracle) mode the empty string
// is NULL, which changes the meaning of every NOT NULL column.
//
// django: db/backends/postgresql/ (openGauss has no Django backend)
package gaussdb

import (
	"fmt"
	"strings"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/backends/postgresql"
)

// Features are PostgreSQL's values minus what openGauss 7.0 does not
// implement. Every flag below was verified against the server.
var Features = func() base.Features {
	f := postgresql.Features
	// openGauss accepts CREATE EXTENSION only for the extensions built
	// into its own installation -- none of the ones with a shortcut here
	// -- has no DROP EXTENSION, and has no CREATE COLLATION.
	f.SupportsExtensions = false
	f.SupportsCollations = false
	// "ERROR: create a index with include columns is only supported in
	// ubtree": INCLUDE needs the ubtree access method, which is reserved
	// for Ustore tables, so covering indexes are not generally available.
	f.SupportsCoveringIndexes = false
	// UNIQUE ... NULLS NOT DISTINCT is PostgreSQL 15 syntax:
	// "ERROR: syntax error at or near "not"".
	f.SupportsNullsDistinctUniqueConstraints = false
	// An ALTER TABLE with several sub-commands is rejected on a column
	// whose type was already altered earlier in the same transaction:
	// "ERROR: cannot alter type of column "name" twice". The same
	// sub-commands as separate statements are accepted, and so are
	// repeated type changes, so only the combined form is unavailable.
	// This is also Django's default value for the flag; PostgreSQL is
	// the backend that overrides it.
	f.SupportsCombinedAlters = false
	return f
}()

// Backend is the openGauss backend definition.
var Backend = &base.Backend{
	Vendor:      "gaussdb",
	DisplayName: "openGauss",
	Features:    Features,
	// openGauss keeps PostgreSQL's NAMEDATALEN; gorm.io/driver/gaussdb
	// uses 63 as its identifier limit too.
	MaxNameLength:    63,
	NewIntrospection: func(c *base.Conn) base.Introspection { return NewIntrospection(c) },
	SequenceResetSQL: postgresql.SequenceResetSQL,
	Ops:              Ops{},
}

func init() {
	Backend.NewEditor = func(c *base.Conn, collect, atomic bool) base.SchemaEditor {
		return NewEditor(c, collect, atomic)
	}
	base.Register(base.Detector{
		Name:         "gaussdb",
		Dialector:    "gaussdb",
		VersionQuery: "SELECT version()",
		Check:        CheckCompatibility,
		Backend:      Backend,
	})
}

// CheckCompatibility rejects databases that are not in PostgreSQL
// compatibility mode. openGauss chooses the semantics of a database at
// CREATE DATABASE time; in the default DBCOMPATIBILITY 'A' (Oracle) mode
// the empty string is stored as NULL, so a NOT NULL column rejects values
// gormgate and gorm consider valid, and introspection of defaults differs.
func CheckCompatibility(c *base.Conn, version string) error {
	var compat string
	err := c.DB().Raw("SELECT datcompatibility FROM pg_database WHERE datname = current_database()").Row().Scan(&compat)
	if err != nil {
		return fmt.Errorf("gormgate: reading the openGauss compatibility mode of the current database: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(compat), "PG") {
		return fmt.Errorf("gormgate: the openGauss database is in DBCOMPATIBILITY '%s' mode; "+
			"gormgate requires 'PG' (PostgreSQL) compatibility, because in the other modes the empty string is NULL "+
			"and NOT NULL columns, defaults and introspection behave differently. "+
			"Recreate the database with: CREATE DATABASE <name> DBCOMPATIBILITY 'PG'", strings.TrimSpace(compat))
	}
	return nil
}

// Ops are openGauss's connection.ops helpers; quoting, literals and
// transaction SQL are PostgreSQL's (standard_conforming_strings is on and
// bytea accepts the hex format).
type Ops struct{ postgresql.Ops }

// Editor is the openGauss schema editor.
type Editor struct {
	*postgresql.Editor
}

// A method whose name does not match a step of base.Overrides is a
// method nothing calls; this makes that a build failure.
var _ base.Overrides = (*Editor)(nil)

// NewEditor builds an openGauss schema editor.
//
// openGauss has neither CREATE/ALTER SEQUENCE ... AS <type> ("ERROR: syntax
// error at or near "AS"", PostgreSQL 10 syntax) nor identity columns, so
// the PostgreSQL editor gets the zero SerialSQL: sequences are created
// untyped, a serial-to-serial width change needs no sequence change at all,
// and altering a column into or out of an identity is an error.
func NewEditor(c *base.Conn, collect, atomic bool) *Editor {
	e := &Editor{}
	e.Editor = postgresql.NewEmbeddedEditor(e, c, postgresql.SerialSQL{},
		base.EditorOptions{CollectSQL: collect, Atomic: atomic})
	return e
}
