package base

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gorm.io/gorm"

	m "github.com/ctolon/gormgate/migrations"
)

// Router decides whether a model may be migrated on a database (Django's
// database routers' allow_migrate). A nil result means "no opinion".
type Router interface {
	AllowMigrate(db, app string, h m.Hints) *bool
}

// Conn is one pinned database connection used by a command. All statements
// run on the same session so that session settings (SQLite PRAGMAs,
// transaction state) are preserved.
type Conn struct {
	alias   string
	root    *gorm.DB
	db      *gorm.DB
	sqlConn *sql.Conn
	Backend *Backend
	// Version is the server version string reported by the database.
	Version string
	Routers []Router

	atomicDepth int
	intro       Introspection
}

// Open pins a connection of db for alias and detects its backend.
func Open(ctx context.Context, alias string, db *gorm.DB, routers []Router) (*Conn, error) {
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("gormgate: database '%s': %w", alias, err)
	}
	sc, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("gormgate: database '%s': %w", alias, err)
	}
	pinned := db.Session(&gorm.Session{NewDB: true, SkipDefaultTransaction: true, Context: ctx})
	pinned.Statement.ConnPool = sc
	c := &Conn{alias: alias, root: pinned, db: pinned, sqlConn: sc, Routers: routers}
	b, version, err := Detect(c)
	if err != nil {
		sc.Close()
		return nil, err
	}
	c.Backend, c.Version = b, version
	if b.InitConnectionState != nil {
		session, err := b.InitConnectionState(c, c.root)
		if err != nil {
			sc.Close()
			return nil, fmt.Errorf("gormgate: database '%s': %w", alias, err)
		}
		if session != nil {
			c.root, c.db = session, session
		}
	}
	return c, nil
}

// Close releases the pinned connection.
func (c *Conn) Close() error {
	if c.sqlConn == nil {
		return nil
	}
	err := c.sqlConn.Close()
	c.sqlConn = nil
	return err
}

// Alias is the settings name of the database.
func (c *Conn) Alias() string { return c.alias }

// Vendor is the backend vendor name ("postgresql", "mysql", ...).
func (c *Conn) Vendor() string { return string(c.Backend.Vendor) }

// DB returns the current handle (the transaction inside Atomic).
func (c *Conn) DB() *gorm.DB { return c.db }

// Features returns the backend features.
func (c *Conn) Features() *Features { return &c.Backend.Features }

// InAtomicBlock reports whether a transaction is open on this connection.
func (c *Conn) InAtomicBlock() bool { return c.atomicDepth > 0 }

// Introspection returns the backend introspection bound to this connection.
func (c *Conn) Introspection() Introspection {
	if c.intro == nil {
		c.intro = c.Backend.NewIntrospection(c)
	}
	return c.intro
}

// AllowMigrate consults the routers in order; the first opinion wins and
// the default is true.
//
// django: db/utils.py ConnectionRouter.allow_migrate
func (c *Conn) AllowMigrate(app string, h m.Hints) bool {
	for _, r := range c.Routers {
		if v := r.AllowMigrate(c.alias, app, h); v != nil {
			return *v
		}
	}
	return true
}

// Atomic runs fn in a transaction, nesting with savepoints.
//
// django: db/transaction.py atomic
func (c *Conn) Atomic(fn func() error) (err error) {
	if !c.Backend.Features.SupportsTransactions {
		return fn()
	}
	outer := c.db
	c.atomicDepth++
	defer func() { c.atomicDepth--; c.db = outer }()
	return outer.Transaction(func(tx *gorm.DB) error {
		c.db = tx
		return fn()
	})
}

// Exec runs a statement.
func (c *Conn) Exec(sql string, args ...any) error {
	return c.db.Exec(sql, args...).Error
}

// Rows runs a query and returns the raw rows.
func (c *Conn) Rows(query string, args ...any) (*sql.Rows, error) {
	return c.db.Raw(query, args...).Rows()
}

// ErrNoBackend is returned when no imported backend recognizes the
// database. Callers branch on it with errors.Is to tell "you forgot to
// import a backend" apart from a connection or a server problem.
var ErrNoBackend = errors.New("gormgate: no backend matches this database")

var _ m.Connection = (*Conn)(nil)
