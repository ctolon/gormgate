// Package tidb is the TiDB backend. TiDB speaks the MySQL protocol and is
// reached through gorm.io/driver/mysql, so it reuses the MySQL schema
// editor, operations and introspection and only replaces what TiDB does
// differently.
//
// Reference: django-tidb (the third-party Django backend for TiDB).
package tidb

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/backends/mysql"
	m "github.com/ctolon/gormgate/migrations"
)

// Server describes the connected TiDB server.
type Server struct {
	// Version is the TiDB version (the "v8.5.1" of "8.0.11-TiDB-v8.5.1").
	Version mysql.Version
	// CheckConstraints is @@tidb_enable_check_constraint: TiDB parses CHECK
	// constraints but only creates them when this is on.
	CheckConstraints bool
	// MySQL is the MySQL-compatible server description the shared MySQL
	// editor and introspection work with.
	MySQL mysql.Server
}

var versionRe = regexp.MustCompile(`TiDB-v(\d+)\.(\d+)\.(\d+)`)

// ParseVersion extracts the TiDB version of a VERSION() string such as
// "8.0.11-TiDB-v8.5.1".
func ParseVersion(s string) (mysql.Version, bool) {
	mt := versionRe.FindStringSubmatch(s)
	if mt == nil {
		return mysql.Version{}, false
	}
	var v mysql.Version
	for i := range v {
		v[i], _ = strconv.Atoi(mt[i+1])
	}
	return v, true
}

// ForeignKeyVersion is the first TiDB release with foreign keys.
var ForeignKeyVersion = mysql.Version{6, 6, 0}

// ReadServer collects everything the backend needs from the server.
func ReadServer(c *base.Conn, version string) (Server, error) {
	v, ok := ParseVersion(version)
	if !ok {
		return Server{}, fmt.Errorf("gormgate: cannot determine the TiDB version from %q", version)
	}
	my, err := mysql.ReadServer(c, version)
	if err != nil {
		return Server{}, err
	}
	s := Server{Version: v, MySQL: my}
	var check sql.NullBool
	if err := c.DB().Raw("SELECT @@tidb_enable_check_constraint").Row().Scan(&check); err != nil {
		return Server{}, fmt.Errorf("gormgate: reading @@tidb_enable_check_constraint: %w", err)
	}
	s.CheckConstraints = check.Bool
	return s, nil
}

// FeaturesFor adjusts the MySQL features to what TiDB really does.
func FeaturesFor(s Server) base.Features {
	f := mysql.FeaturesFor(s.MySQL)
	// Foreign keys became a supported feature in TiDB 6.6.
	f.SupportsForeignKeys = s.Version.AtLeast(ForeignKeyVersion[0], ForeignKeyVersion[1], ForeignKeyVersion[2])
	// CHECK constraints are parsed but silently dropped unless
	// tidb_enable_check_constraint is on.
	f.SupportsTableCheckConstraints = s.CheckConstraints
	// TiDB accepts DESC in an index definition but stores the index
	// ascending, so an index column order can't be expressed.
	f.SupportsIndexColumnOrdering = false
	// TiDB has expression indexes over the functions listed in
	// tidb_allow_function_for_expression_index.
	f.SupportsExpressionIndexes = true
	// A clustered primary key is the row key itself, so its integer columns
	// can't change type (and can't gain or lose AUTO_INCREMENT).
	f.CannotAlterIntegerPrimaryKeyType = true
	return f
}

var backends sync.Map // Server -> *base.Backend

// BackendFor returns the (cached) backend of a TiDB server.
func BackendFor(s Server) *base.Backend {
	if b, ok := backends.Load(s); ok {
		return b.(*base.Backend)
	}
	b := &base.Backend{
		Vendor:        "tidb",
		DisplayName:   "TiDB",
		Features:      FeaturesFor(s),
		MaxNameLength: mysql.MaxNameLength,
		NewEditor: func(c *base.Conn, collect, atomic bool) base.SchemaEditor {
			return NewEditor(c, s, collect, atomic)
		},
		NewIntrospection: func(c *base.Conn) base.Introspection { return NewIntrospection(c, s) },
		SequenceResetSQL: mysql.SequenceResetSQL,
		Ops:              mysql.Ops{},
	}
	actual, _ := backends.LoadOrStore(s, b)
	return actual.(*base.Backend)
}

// NewIntrospection builds the TiDB introspection: TiDB's
// information_schema.check_constraints has no table_name column, so its own
// tidb_check_constraints view is used instead.
func NewIntrospection(c *base.Conn, s Server) *mysql.Introspection {
	i := mysql.NewIntrospection(c, s.MySQL)
	i.CheckConstraintQuery = ""
	if s.CheckConstraints {
		i.CheckConstraintQuery = `
			SELECT c.constraint_name, c.check_clause
			FROM information_schema.tidb_check_constraints AS c
			WHERE c.constraint_schema = DATABASE() AND c.table_name = ?`
	}
	return i
}

// Editor is the TiDB schema editor: the MySQL one, with the alterations
// TiDB cannot perform rejected up front.
type Editor struct {
	*mysql.Editor
	Server Server
}

// A method whose name does not match a step of base.Overrides is a
// method nothing calls; this makes that a build failure.
var _ base.Overrides = (*Editor)(nil)

// NewEditor builds a TiDB schema editor.
func NewEditor(c *base.Conn, s Server, collect, atomic bool) *Editor {
	e := &Editor{Server: s}
	e.Editor = mysql.NewEmbeddedEditor(e, c, s.MySQL, base.EditorOptions{CollectSQL: collect, Atomic: atomic})
	return e
}

// isClusteredIntegerPK reports whether f is the single integer primary key
// column of its model, which TiDB stores as the clustered row key.
func isClusteredIntegerPK(model *m.Model, f *m.ModelField) bool {
	if !f.Field.PrimaryKey || (f.Field.Type != m.Int && f.Field.Type != m.Uint) {
		return false
	}
	return len(model.PK()) == 1
}

// AlterColumnTypeSQL rejects the type changes TiDB refuses on a clustered
// integer primary key instead of letting the server fail halfway through a
// migration that cannot be rolled back.
func (e *Editor) AlterColumnTypeSQL(model *m.Model, old, new *m.ModelField, newType string) (base.Fragment, []string, error) {
	oldType, err := e.ColumnType(model, old)
	if err != nil {
		return base.Fragment{}, nil, err
	}
	if oldType != newType && isClusteredIntegerPK(model, old) && isClusteredIntegerPK(model, new) {
		return base.Fragment{}, nil, &m.NotSupportedError{Msg: fmt.Sprintf(
			"TiDB cannot change the type of %s.%s from %q to %q: the column is the clustered integer primary key of the table, "+
				"which MODIFY rejects with \"Unsupported modify column: this column has primary key flag\" "+
				"(and, when AUTO_INCREMENT is involved, \"can't set auto_increment\"). "+
				"Recreate the model with the new primary key instead.",
			model.Table, old.Column, oldType, newType)}
	}
	return e.Editor.AlterColumnTypeSQL(model, old, new, newType)
}

func resolve(c *base.Conn, version string) (*base.Backend, error) {
	s, err := ReadServer(c, version)
	if err != nil {
		return nil, err
	}
	return BackendFor(s), nil
}

func init() {
	base.Register(base.Detector{
		Name:         "tidb",
		Priority:     base.PriorityFork,
		Dialector:    "mysql",
		VersionQuery: mysql.VersionQuery,
		Match:        func(v string) bool { return strings.Contains(v, "TiDB") },
		Resolve:      resolve,
	})
}
