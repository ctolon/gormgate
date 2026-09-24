package migrations

import ()

// django: migrations/operations/special.py

// SeparateDatabaseAndState applies one list of operations to the database
// and another to the state.
type SeparateDatabaseAndState struct {
	baseOp
	DatabaseOperations []Operation
	StateOperations    []Operation
}

func (o *SeparateDatabaseAndState) Category() Category { return CategoryMixed }

func (o *SeparateDatabaseAndState) StateForwards(app string, s *ProjectState) error {
	for _, op := range o.StateOperations {
		if err := op.StateForwards(app, s); err != nil {
			return err
		}
	}
	return nil
}

// DatabaseForwards runs the database operations in order. Each one sees the
// state its predecessors produced, so the states are recomputed here instead
// of using the to state the caller passed in.
//
// django: operations/special.py SeparateDatabaseAndState.database_forwards
func (o *SeparateDatabaseAndState) DatabaseForwards(app string, ed SchemaEditor, from, _ *ProjectState) error {
	before := from
	for _, op := range o.DatabaseOperations {
		after := before.Clone()
		if err := op.StateForwards(app, after); err != nil {
			return err
		}
		if err := op.DatabaseForwards(app, ed, before, after); err != nil {
			return err
		}
		before = after
	}
	return nil
}

// DatabaseBackwards unapplies the database operations in reverse. The first
// loop records the state each operation starts from and advances to the
// state after the last one; the second loop walks those states backwards,
// reversing each operation between the pair of states it ran between.
//
// django: operations/special.py SeparateDatabaseAndState.database_backwards
func (o *SeparateDatabaseAndState) DatabaseBackwards(app string, ed SchemaEditor, _, to *ProjectState) error {
	before := make([]*ProjectState, len(o.DatabaseOperations))
	after := to
	for i, op := range o.DatabaseOperations {
		before[i] = after
		after = after.Clone()
		if err := op.StateForwards(app, after); err != nil {
			return err
		}
	}
	for i := len(o.DatabaseOperations) - 1; i >= 0; i-- {
		if err := o.DatabaseOperations[i].DatabaseBackwards(app, ed, after, before[i]); err != nil {
			return err
		}
		after = before[i]
	}
	return nil
}

func (o *SeparateDatabaseAndState) ReferencesField(model, name, app string) bool { return true }
func (o *SeparateDatabaseAndState) Describe() string {
	return "custom state/database change combination"
}
func (o *SeparateDatabaseAndState) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	return baseReduce(o, other)
}

// SQLParams is one SQL statement together with its bind parameters.
type SQLParams struct {
	// SQL is the statement, with the backend's placeholders.
	SQL string
	// Params are the values bound to those placeholders.
	Params []any
}

// SQL is what a RunSQL operation runs. It is one of Script, Statements or
// Parameterized; the interface is sealed, so nothing else can be one and a
// wrong value is a compile error rather than a failure part way through a
// migration.
type SQL interface{ isSQL() }

// Script is an SQL script the backend splits into statements the way its
// own client would.
type Script string

// Statements are SQL statements, each run as it is.
type Statements []string

// Parameterized are SQL statements with values bound to their
// placeholders.
type Parameterized []SQLParams

func (Script) isSQL()        {}
func (Statements) isSQL()    {}
func (Parameterized) isSQL() {}

// NoSQL is a reverse that does nothing. It makes an operation reversible
// without running anything, for a forwards SQL that needs no undoing.
var NoSQL = Statements{}

// RunSQL runs raw SQL. A nil ReverseSQL makes the operation irreversible;
// NoSQL makes it a reversible no-op.
type RunSQL struct {
	baseOp
	SQL             SQL
	ReverseSQL      SQL
	StateOperations []Operation
	Hints           map[string]any
	IsElidable      bool
}

func (o *RunSQL) Category() Category { return CategorySQL }

// Reversible reports whether a reverse was given at all. An empty one
// (NoSQL) is reversible and runs nothing.
func (o *RunSQL) Reversible() bool { return o.ReverseSQL != nil }
func (o *RunSQL) Elidable() bool   { return o.IsElidable }

func (o *RunSQL) StateForwards(app string, s *ProjectState) error {
	for _, op := range o.StateOperations {
		if err := op.StateForwards(app, s); err != nil {
			return err
		}
	}
	return nil
}

func (o *RunSQL) DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	if routerAllowMigrate(ed.Connection(), app, o.Hints) {
		return runSQL(ed, o.SQL)
	}
	return nil
}

func (o *RunSQL) DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	if !o.Reversible() {
		return &IrreversibleError{Msg: "you cannot reverse this operation"}
	}
	if routerAllowMigrate(ed.Connection(), app, o.Hints) {
		return runSQL(ed, o.ReverseSQL)
	}
	return nil
}

func (o *RunSQL) ReferencesField(model, name, app string) bool { return true }
func (o *RunSQL) Describe() string                             { return "raw SQL operation" }
func (o *RunSQL) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	return baseReduce(o, other)
}

// ScriptSplitter splits an SQL script into the individual statements to
// execute. Backends provide it through their schema editor; a schema editor
// that does not implement it runs the script as a single statement.
//
// django: db/backends/base/operations.py
// BaseDatabaseOperations.prepare_sql_script
type ScriptSplitter interface {
	PrepareSQLScript(script string) []string
}

func runSQL(ed SchemaEditor, sqls SQL) error {
	switch x := sqls.(type) {
	case nil:
		return nil
	case Statements:
		for _, s := range x {
			if err := ed.Execute(s); err != nil {
				return err
			}
		}
	case Parameterized:
		for _, s := range x {
			if err := ed.Execute(s.SQL, s.Params...); err != nil {
				return err
			}
		}
	case Script:
		if x == "" {
			return nil
		}
		statements := []string{string(x)}
		if sp, ok := ed.(ScriptSplitter); ok {
			statements = sp.PrepareSQLScript(string(x))
		}
		for _, s := range statements {
			if err := ed.Execute(s); err != nil {
				return err
			}
		}
	}
	return nil
}

// RunGoFunc is the signature of the code a RunGo operation runs. It is
// given the models as they stood at this point in the migration history,
// and the schema editor of the connection being migrated.
//
// django: operations/special.py RunPython (code(apps, schema_editor))
type RunGoFunc func(apps *Apps, schemaEditor SchemaEditor) error

// RunGoNoop is a RunGoFunc that does nothing. Use it as ReverseCode to make
// a RunGo operation reversible without undoing anything.
//
// django: operations/special.py RunPython.noop
func RunGoNoop(apps *Apps, schemaEditor SchemaEditor) error { return nil }

// RunGo runs Go code against the models as they stood at this point in the
// migration history.
//
// django: operations/special.py RunPython
type RunGo struct {
	baseOp
	Code        RunGoFunc
	ReverseCode RunGoFunc
	// IsAtomic is nil for the default (use the migration's atomic).
	IsAtomic   *bool
	Hints      map[string]any
	IsElidable bool
}

func (o *RunGo) Category() Category { return CategoryCode }
func (o *RunGo) Reversible() bool   { return o.ReverseCode != nil }
func (o *RunGo) ReducesToSQL() bool { return false }
func (o *RunGo) Atomic() *bool      { return o.IsAtomic }
func (o *RunGo) Elidable() bool     { return o.IsElidable }

func (o *RunGo) StateForwards(string, *ProjectState) error { return nil }

func (o *RunGo) DatabaseForwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	if o.Code == nil {
		return &ValueError{Msg: "RunGo must be supplied with a callable"}
	}
	if !routerAllowMigrate(ed.Connection(), app, o.Hints) {
		return nil
	}
	apps, err := from.Apps()
	if err != nil {
		return err
	}
	return o.Code(apps, ed)
}

func (o *RunGo) DatabaseBackwards(app string, ed SchemaEditor, from, to *ProjectState) error {
	if o.ReverseCode == nil {
		return &IrreversibleError{Msg: "you cannot reverse this operation"}
	}
	if !routerAllowMigrate(ed.Connection(), app, o.Hints) {
		return nil
	}
	apps, err := from.Apps()
	if err != nil {
		return err
	}
	return o.ReverseCode(apps, ed)
}

func (o *RunGo) ReferencesField(model, name, app string) bool { return true }
func (o *RunGo) Describe() string                             { return "raw Go operation" }
func (o *RunGo) Reduce(other Operation, app string) ([]Operation, ReduceKind) {
	return baseReduce(o, other)
}

// routerAllowMigrate asks the routers whether this operation may run. The
// operation's own hints are passed through as Hints.Extra. A RunSQL or
// RunGo names no model, so the typed fields stay empty.
func routerAllowMigrate(conn Connection, app string, hints map[string]any) bool {
	return conn.AllowMigrate(app, Hints{Extra: hints})
}
