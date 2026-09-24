// Package fromgorm turns the gorm models an application declares into the
// migration model states the autodetector compares against the migration
// history.
//
// django: db/migrations/state.py ModelState.from_model,
// ProjectState.from_apps
package fromgorm

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	m "github.com/ctolon/gormgate/migrations"
)

// CheckError is a problem with a model, reported by the system checks in
// the form the checks framework prints.
//
// The identifiers are stable and are never reused.  gormgate's own are
// numbered in three ranges -- E for errors, W for warnings, I for
// information -- and the whole scheme is recorded here, wherever the check
// that reports one lives:
//
//	gormgate.E001  a connection that cannot be opened (checks.go)
//	gormgate.E002  Settings.NamingStrategy is not the connection's namer
//	gormgate.E003  the backend refuses a model outright
//	gormgate.E300  the models do not convert to a project state
//	gormgate.E310  retired: "composite foreign key is not supported", which
//	               ended when composite foreign keys became a
//	               ForeignKeyConstraint.  The number stays free.
//	gormgate.E311  a relation to a model no app registers
//	gormgate.E312  one column is the foreign key of two different relations
//	gormgate.W001  the backend will silently leave an object out
//	gormgate.W002  the backend will refuse a later change to a model
//	gormgate.I001  what gormgate owns in the project
//
// E001, E002, E003, W001, W002 and I001 are reported by the checks in
// internal/management; the rest come from this package.
type CheckError struct {
	// ID is the check identifier, such as "gormgate.E311".
	ID string
	// Obj names the model or field the problem is about.
	Obj string
	// Msg describes the problem.
	Msg string
}

func (e *CheckError) Error() string { return e.Obj + ": (" + e.ID + ") " + e.Msg }

// App is an app label together with its models, given as pointers to
// structs.
type App struct {
	Label  string
	Models []any
}

// Config controls how models are interpreted; it must match the gorm.Config
// the application uses.
type Config struct {
	Namer schema.Namer
	// DisableForeignKeyConstraintWhenMigrating mirrors gorm.Config.
	DisableForeignKeyConstraintWhenMigrating bool
	// IgnoreRelationshipsWhenMigrating mirrors gorm.Config.
	IgnoreRelationshipsWhenMigrating bool
}

// typeTags are the tag settings dialects consult in DataTypeOf.
var typeTags = []string{"INDEX", "PRECISION", "GENERATED", "CODEC", "TTL"}

type parsed struct {
	app    string
	model  any
	schema *schema.Schema
}

var gormDBDataType = reflect.TypeOf((*interface {
	GormDBDataType(*gorm.DB, *schema.Field) string
})(nil)).Elem()

// ProjectState builds the current project state from the registered apps.
//
// django: state.py ProjectState.from_apps
func ProjectState(apps []App, cfg Config) (*m.ProjectState, error) {
	if cfg.Namer == nil {
		cfg.Namer = schema.NamingStrategy{}
	}
	cache := &sync.Map{}
	var all []parsed
	byType := map[reflect.Type]parsed{}
	for _, a := range apps {
		for _, model := range a.Models {
			s, err := schema.Parse(model, cache, cfg.Namer)
			if err != nil {
				return nil, fmt.Errorf("gormgate: parsing model %T of app '%s': %w", model, a.Label, err)
			}
			p := parsed{app: a.Label, model: model, schema: s}
			if prev, dup := byType[s.ModelType]; dup {
				return nil, fmt.Errorf("gormgate: model %s is registered in both app '%s' and app '%s'", s.Name, prev.app, a.Label)
			}
			byType[s.ModelType] = p
			all = append(all, p)
		}
	}
	state := m.NewProjectState()
	tables := map[string]m.ModelKey{}
	for _, p := range all {
		ms, err := modelState(p, byType, cfg)
		if err != nil {
			return nil, err
		}
		if prev, ok := state.Models[ms.Key()]; ok {
			return nil, fmt.Errorf("gormgate: app '%s' has two models named %s", ms.App, prev.Name)
		}
		// Only managed models own a table. An unmanaged one is declared
		// so that migrations know it exists without creating or altering
		// it, which is how a second type over the same table -- a
		// database view, or the nearest thing to a Django proxy model --
		// is mapped. Django's check counts the same set.
		//
		// django: core/checks/model_checks.py check_all_models
		if ms.Options.IsManaged() {
			if other, ok := tables[ms.Table]; ok {
				return nil, fmt.Errorf("gormgate: models %s and %s.%s use the same table '%s'", other, ms.App, ms.Name, ms.Table)
			}
			tables[ms.Table] = ms.Key()
		}
		state.Models[ms.Key()] = ms
	}
	if cfg.IgnoreRelationshipsWhenMigrating {
		return state, nil
	}
	// Implicit many2many join tables, owned by the first declaring model in
	// registration order.
	for _, p := range all {
		for _, name := range sortedRelationNames(p.schema) {
			rel := p.schema.Relationships.Relations[name]
			if rel.JoinTable == nil || rel.Field.IgnoreMigration {
				continue
			}
			jt := rel.JoinTable
			if _, taken := tables[jt.Table]; taken {
				continue
			}
			ms, err := joinTableState(p.app, jt, byType, cfg)
			if err != nil {
				return nil, err
			}
			tables[jt.Table] = ms.Key()
			state.Models[ms.Key()] = ms
		}
	}
	return state, nil
}

func sortedRelationNames(s *schema.Schema) []string {
	return slices.Sorted(maps.Keys(s.Relationships.Relations))
}

func label(p parsed) string { return p.app + "." + strings.ToLower(p.schema.Name) }

func modelState(p parsed, byType map[reflect.Type]parsed, cfg Config) (*m.ModelState, error) {
	s := p.schema
	ms := &m.ModelState{App: p.app, Name: s.Name, Table: s.Table}
	for _, db := range s.DBNames {
		f := s.FieldsByDBName[db]
		if f.IgnoreMigration {
			continue
		}
		mf, err := field(f)
		if err != nil {
			return nil, fmt.Errorf("gormgate: %s.%s.%s: %w", p.app, s.Name, f.Name, err)
		}
		ms.Fields = append(ms.Fields, m.NamedField{Name: db, Field: mf})
	}
	if !cfg.DisableForeignKeyConstraintWhenMigrating && !cfg.IgnoreRelationshipsWhenMigrating {
		if err := attachForeignKeys(ms, s, byType); err != nil {
			return nil, err
		}
	}
	for _, idx := range s.ParseIndexes() {
		ms.Options.Indexes = append(ms.Options.Indexes, index(idx))
	}
	checks := s.ParseCheckConstraints()
	for _, name := range slices.Sorted(maps.Keys(checks)) {
		ms.Options.Constraints = append(ms.Options.Constraints, &m.CheckConstraint{Name: name, Check: checks[name].Constraint})
	}
	if mp, ok := p.model.(m.MetaProvider); ok {
		meta := mp.MigrationMeta()
		ms.Options.Managed = meta.Managed
		ms.Options.DBTableComment = meta.DBTableComment
		ms.Options.UniqueTogether = meta.UniqueTogether
		ms.Options.Indexes = append(ms.Options.Indexes, meta.Indexes...)
		ms.Options.Constraints = append(ms.Options.Constraints, meta.Constraints...)
		ms.Options.ClickHouse = meta.ClickHouse
		// MigrationMeta hands out the app's own slices; the state owns
		// what it holds, and ProjectState.RenameField rewrites index
		// and unique_together entries in place.
		ms.Options = ms.Options.Clone()
	}
	if err := ms.Validate(); err != nil {
		return nil, err
	}
	return ms, nil
}

// attachForeignKeys records the constraints gorm's CreateTable would build
// for this table: every relation whose constraint lives on this schema,
// including has-one/has-many relations gorm injects into the child.
func attachForeignKeys(ms *m.ModelState, s *schema.Schema, byType map[reflect.Type]parsed) error {
	for _, name := range sortedRelationNames(s) {
		rel := s.Relationships.Relations[name]
		if rel.Field == nil || rel.Field.IgnoreMigration {
			continue
		}
		c := rel.ParseConstraint()
		if c == nil || c.Schema != s {
			continue
		}
		target, ok := byType[c.ReferenceSchema.ModelType]
		if !ok {
			return &CheckError{ID: "gormgate.E311", Obj: ms.App + "." + ms.Name, Msg: fmt.Sprintf("relation %s references model %s, which is not registered in any app", rel.Name, c.ReferenceSchema.Name)}
		}
		columns := make([]string, len(c.ForeignKeys))
		refColumns := make([]string, len(c.References))
		known := true
		for i, f := range c.ForeignKeys {
			columns[i] = f.DBName
			known = known && ms.Fields.Index(f.DBName) >= 0
		}
		for i, f := range c.References {
			refColumns[i] = f.DBName
		}
		// A key over a column the model does not migrate cannot be
		// created; gorm builds the same broken constraint, so there is
		// nothing to be in parity with.
		if !known || len(columns) == 0 {
			continue
		}
		onDelete := m.ReferentialAction(strings.ToUpper(c.OnDelete))
		onUpdate := m.ReferentialAction(strings.ToUpper(c.OnUpdate))
		if len(columns) > 1 {
			// A key over several columns does not fit on a field: it
			// becomes a table constraint, named exactly as gorm names it.
			// Two relations describing the same key declare it once, as
			// two relations sharing a single-column key do below.
			if slices.ContainsFunc(ms.Options.Constraints, func(x m.Constraint) bool { return x.ConstraintName() == c.Name }) {
				continue
			}
			ms.Options.Constraints = append(ms.Options.Constraints, &m.ForeignKeyConstraint{
				Name:     c.Name,
				Fields:   columns,
				To:       label(target),
				ToFields: refColumns,
				OnDelete: onDelete,
				OnUpdate: onUpdate,
			})
			continue
		}
		col := columns[0]
		i := ms.Fields.Index(col)
		fk := &m.ForeignKey{
			To:       label(target),
			ToField:  refColumns[0],
			OnDelete: onDelete,
			OnUpdate: onUpdate,
			Name:     c.Name,
		}
		if prev := ms.Fields[i].Field.ForeignKey; prev != nil {
			if prev.To != fk.To || prev.ToField != fk.ToField {
				return &CheckError{ID: "gormgate.E312", Obj: ms.App + "." + ms.Name, Msg: fmt.Sprintf("field %s is the foreign key of two relations to different targets (%s, %s)", col, prev.To, fk.To)}
			}
			continue
		}
		ms.Fields[i].Field.ForeignKey = fk
	}
	return nil
}

func joinTableState(app string, jt *schema.Schema, byType map[reflect.Type]parsed, cfg Config) (*m.ModelState, error) {
	ms := &m.ModelState{App: app, Name: jt.Name, Table: jt.Table, Options: m.Options{AutoCreated: true}}
	for _, db := range jt.DBNames {
		f := jt.FieldsByDBName[db]
		mf, err := field(f)
		if err != nil {
			return nil, fmt.Errorf("gormgate: join table %s.%s: %w", jt.Table, f.Name, err)
		}
		ms.Fields = append(ms.Fields, m.NamedField{Name: db, Field: mf})
	}
	if !cfg.DisableForeignKeyConstraintWhenMigrating {
		if err := attachForeignKeys(ms, jt, byType); err != nil {
			return nil, err
		}
	}
	for _, idx := range jt.ParseIndexes() {
		ms.Options.Indexes = append(ms.Options.Indexes, index(idx))
	}
	return ms, ms.Validate()
}

// field converts one gorm field.
func field(f *schema.Field) (m.Field, error) {
	if f.DataType == "" {
		return m.Field{}, errors.New("field has no data type")
	}
	mf := m.Field{
		Type:          m.DataType(f.DataType),
		Size:          f.Size,
		Precision:     f.Precision,
		Scale:         f.Scale,
		PrimaryKey:    f.PrimaryKey,
		AutoIncrement: f.AutoIncrement,
		Null:          !f.NotNull,
		Unique:        f.Unique,
		Comment:       f.Comment,
		AutoNowAdd:    f.AutoCreateTime > 0,
		AutoNow:       f.AutoUpdateTime > 0,
	}
	if f.AutoIncrement && f.AutoIncrementIncrement != schema.DefaultAutoIncrementIncrement {
		mf.AutoIncrementIncrement = f.AutoIncrementIncrement
	}
	if name := f.TagSettings["SERIALIZER"]; name != "" {
		mf.Serializer = name
	} else if name := f.TagSettings["JSON"]; name != "" {
		mf.Serializer = name
	}
	for _, t := range typeTags {
		v, ok := f.TagSettings[t]
		if !ok || v == "" {
			continue
		}
		if mf.Tags == nil {
			mf.Tags = map[string]string{}
		}
		if t == "INDEX" {
			// Dialects only test the presence of the INDEX tag; the index
			// itself (and its name) is recorded in Options.Indexes.
			v = "INDEX"
		}
		mf.Tags[t] = v
	}
	if f.IndirectFieldType != nil && reflect.PointerTo(f.IndirectFieldType).Implements(gormDBDataType) && f.IndirectFieldType.Name() != "" {
		t := f.IndirectFieldType
		mf.Custom = &m.TypeRef{Package: t.PkgPath(), Name: t.Name(), Type: t}
	}
	d, err := dbDefault(f)
	if err != nil {
		return mf, err
	}
	mf.DBDefault = d
	return mf, nil
}

// dbDefault reproduces gorm's Migrator.FullDataTypeOf default handling: a
// parsed DefaultValueInterface is a literal, any other non-empty tag value
// except "(-)" is emitted verbatim.
func dbDefault(f *schema.Field) (*m.DBDefault, error) {
	if !f.HasDefaultValue || (f.DefaultValueInterface == nil && f.DefaultValue == "") {
		return nil, nil
	}
	if f.DefaultValueInterface != nil {
		switch v := f.DefaultValueInterface.(type) {
		case bool, int64, uint64, float64, string:
			return m.DBValue(v), nil
		case time.Time:
			return m.DBValue(v), nil
		default:
			return nil, fmt.Errorf("unsupported default value type %T", v)
		}
	}
	if f.DefaultValue == "(-)" {
		return nil, nil
	}
	return m.DBExpr(f.DefaultValue), nil
}

func index(idx *schema.Index) m.Index {
	out := m.Index{
		Name:    idx.Name,
		Type:    m.IndexMethod(idx.Type),
		Where:   idx.Where,
		Comment: idx.Comment,
		Option:  idx.Option,
	}
	switch strings.ToUpper(idx.Class) {
	case "UNIQUE":
		out.Unique = true
	case "":
	default:
		out.Class = m.IndexClass(strings.ToUpper(idx.Class))
	}
	for _, o := range idx.Fields {
		f := m.IndexField{Expression: o.Expression, Sort: m.SortOrder(strings.ToUpper(o.Sort)), Collate: o.Collate, Length: o.Length}
		if o.Expression == "" {
			f.Column = o.DBName
		}
		out.Fields = append(out.Fields, f)
	}
	return out
}
