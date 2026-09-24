// Package base holds what every database backend shares: the connection
// wrapper, the feature flags the migration machinery consults, the
// introspection interface, and the schema editor that renders migration
// operations into DDL. A vendor package fills in a Backend value and
// overrides the schema-editor steps that differ, through Overrides.
//
// django: db/backends/base
package base

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"sync"

	"gorm.io/gorm"

	m "github.com/ctolon/gormgate/migrations"
)

// Vendor is the short name of a database vendor. A migration reads it
// through Connection.Vendor, which reports it as a plain string; inside
// gormgate it is a named type so that a comparison cannot be made against
// a name no backend uses.
type Vendor string

// The vendors gormgate has a backend for.
const (
	PostgreSQL  Vendor = "postgresql"
	CockroachDB Vendor = "cockroachdb"
	GaussDB     Vendor = "gaussdb"
	SQLite      Vendor = "sqlite"
	MySQL       Vendor = "mysql"
	MariaDB     Vendor = "mariadb"
	TiDB        Vendor = "tidb"
	MSSQL       Vendor = "mssql"
	Oracle      Vendor = "oracle"
	ClickHouse  Vendor = "clickhouse"
)

// Backend describes one database vendor: how to talk to it, what it can do,
// and how to build the editor and introspection for a connection to it.
//
// django: db/backends/base/base.py BaseDatabaseWrapper
type Backend struct {
	// Vendor names the database this backend speaks to.
	Vendor Vendor
	// DisplayName is the vendor's name as messages print it.
	DisplayName string
	// Features are the capabilities the migration machinery consults.
	Features Features
	// MaxNameLength is the longest identifier the server accepts; 0 means
	// there is no limit.
	MaxNameLength int
	// RecorderDeleteSettings is appended to the DELETE that unrecords a
	// migration, where the server needs a clause to make the deletion
	// visible to the statement after it. It is here rather than in
	// Features because it is SQL, not a capability.
	RecorderDeleteSettings string

	// NewEditor builds the schema editor for one migration run.
	NewEditor func(c *Conn, collectSQL, atomic bool) SchemaEditor
	// NewIntrospection builds the introspection for a connection.
	NewIntrospection func(c *Conn) Introspection
	// SequenceResetSQL returns the statements that reset the sequences of
	// the given models.
	SequenceResetSQL func(c *Conn, style Style, models []*m.Model) ([]string, error)
	// Ops are the per-vendor quoting and script helpers.
	Ops Ops
	// InitConnectionState prepares the pinned session right after the
	// backend is detected and returns the session to use (nil: keep it).
	//
	// django: base/base.py BaseDatabaseWrapper.init_connection_state
	InitConnectionState func(c *Conn, session *gorm.DB) (*gorm.DB, error)
}

// Style colors the parts of an SQL statement for terminal output.
type Style interface {
	SQLKeyword(string) string
	SQLTable(string) string
	SQLField(string) string
}

// PlainStyle is a Style that leaves every string uncolored.
type PlainStyle struct{}

// SQLKeyword returns s unchanged.
func (PlainStyle) SQLKeyword(s string) string { return s }

// SQLTable returns s unchanged.
func (PlainStyle) SQLTable(s string) string { return s }

// SQLField returns s unchanged.
func (PlainStyle) SQLField(s string) string { return s }

// Ops holds the per-vendor helpers the commands need outside a schema
// editor: identifier quoting, literal rendering and script handling.
//
// django: db/backends/base/operations.py BaseDatabaseOperations
type Ops interface {
	// QuoteName quotes an identifier for this vendor.
	QuoteName(name string) string
	// QuoteValue renders a Go value as an SQL literal.
	QuoteValue(v any) (string, error)
	// PrepareSQLScript splits a script into statements.
	PrepareSQLScript(script string) []string
	// StartTransactionSQL / EndTransactionSQL are used by sqlmigrate.
	StartTransactionSQL() string
	EndTransactionSQL() string
}

// SchemaEditor is a backend schema editor with its lifecycle.
type SchemaEditor interface {
	m.SchemaEditor
	ScriptSplitter
	// Begin opens the migration transaction when atomic.
	Begin() error
	// Finish ends the migration: on success (err == nil) it runs the
	// deferred SQL and commits, otherwise it rolls back.
	//
	// It must return err when err is not nil -- joined with whatever went
	// wrong while finishing, but never swallowed. The executor hands the
	// migration's own error to Finish and decides from the result whether
	// the migration may be recorded as applied, so an implementation that
	// dropped it would record a migration that failed.
	Finish(err error) error
	// Deferred returns the statements Finish still has to run.
	Deferred() []*Statement
	Conn() *Conn
}

// ScriptSplitter splits an SQL script into the statements to run, as a
// RunSQL operation given a single string needs.
type ScriptSplitter interface {
	PrepareSQLScript(script string) []string
}

// Detector recognizes a backend from a gorm dialector name and the server
// version string.
type Detector struct {
	// Name identifies the backend in diagnostics: it is what the error
	// for an unrecognized database lists as available.
	Name string
	// Priority orders detectors for the same dialector (higher first):
	// "tidb" and "mariadb" before "mysql", "cockroachdb" before
	// "postgresql".
	Priority int
	// Dialector is gorm.Dialector.Name().
	Dialector string
	// VersionQuery fetches the version string; empty means no query.
	VersionQuery string
	// Match decides from the version string.
	Match func(version string) bool
	// Check, when set, runs on the connection after Match accepted it and
	// rejects databases the backend can't work with (e.g. an unsupported
	// compatibility mode) with an explanatory error.
	Check func(c *Conn, version string) error
	// Resolve, when set, builds the backend for this connection instead of
	// the static Backend: features that depend on the server version or on
	// server settings (the features Django computes lazily from
	// connection.mysql_version, sql_mode, ...) are computed from it. An
	// error rejects the database (e.g. a version below the minimum).
	Resolve func(c *Conn, version string) (*Backend, error)
	Backend *Backend
}

// Detector priorities, highest first. Detectors that share a dialector are
// tried in this order, so the one that recognizes the most specific server
// gets to answer before the one that would accept anything in the family.
const (
	// PriorityFork is a server that has to be told apart from another
	// fork, not just from the family: TiDB reports a MySQL version string
	// that MariaDB's own matcher would also have to exclude.
	PriorityFork = 30
	// PriorityDerived is a fork of the family: MariaDB, CockroachDB.
	PriorityDerived = 20
	// PriorityFamily is the family itself: MySQL, PostgreSQL.
	PriorityFamily = 10
)

var (
	detectorsMu sync.Mutex
	detectors   []Detector
)

// Register adds a backend detector. A backend package calls it from init,
// so the program's imports decide which databases it recognizes.
//
// It panics on a detector that cannot work: these are programming errors
// in a backend package, and catching them at import time is better than
// finding out when a database is opened.
func Register(d Detector) {
	switch {
	case d.Name == "":
		panic("base.Register: detector has no name")
	case d.Dialector == "":
		panic("base.Register: detector " + d.Name + " names no dialector")
	case (d.Backend == nil) == (d.Resolve == nil):
		panic("base.Register: detector " + d.Name + " must set exactly one of Backend and Resolve")
	}
	detectorsMu.Lock()
	defer detectorsMu.Unlock()
	// One backend may register several dialectors under one name, and
	// several names may share a dialector (mariadb and mysql do), so it
	// is the pair that has to be unique.
	for _, x := range detectors {
		if x.Name == d.Name && x.Dialector == d.Dialector {
			panic("base.Register: duplicate detector " + d.Name + " for dialector " + d.Dialector)
		}
	}
	detectors = append(detectors, d)
	slices.SortStableFunc(detectors, func(a, b Detector) int { return cmp.Compare(b.Priority, a.Priority) })
}

// Detect picks the backend of a connection.
func Detect(c *Conn) (*Backend, string, error) {
	detectorsMu.Lock()
	ds := append([]Detector(nil), detectors...)
	detectorsMu.Unlock()
	name := c.root.Dialector.Name()
	versions := map[string]string{}
	for _, d := range ds {
		if d.Dialector != name {
			continue
		}
		v, seen := versions[d.VersionQuery]
		if !seen && d.VersionQuery != "" {
			var s string
			if err := c.root.Raw(d.VersionQuery).Row().Scan(&s); err != nil {
				return nil, "", fmt.Errorf("gormgate: reading %s server version: %w", name, err)
			}
			v = s
			versions[d.VersionQuery] = v
		}
		if d.Match == nil || d.Match(v) {
			if d.Check != nil {
				if err := d.Check(c, v); err != nil {
					return nil, "", err
				}
			}
			if d.Resolve != nil {
				b, err := d.Resolve(c, v)
				if err != nil {
					return nil, "", err
				}
				return b, v, nil
			}
			return d.Backend, v, nil
		}
	}
	return nil, "", noBackendError(name)
}

// noBackendError is what a caller gets when nothing recognizes the
// database: which dialector was asked for, and which backends the program
// actually imported.
func noBackendError(dialector string) error {
	return fmt.Errorf("%w (gorm dialector %q); import the gormgate backend package for it. Registered: %s",
		ErrNoBackend, dialector, strings.Join(registeredNames(), ", "))
}

// registeredNames lists the backends that have been imported, for the error
// a caller sees when none of them recognizes the database. A backend
// registers itself from its package's init, so the list is exactly what the
// program imported.
func registeredNames() []string {
	detectorsMu.Lock()
	defer detectorsMu.Unlock()
	if len(detectors) == 0 {
		return []string{"none"}
	}
	names := make([]string, 0, len(detectors))
	for _, d := range detectors {
		names = append(names, d.Name)
	}
	slices.Sort(names)
	return slices.Compact(names)
}
