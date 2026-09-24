package migrations

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// DataType is the gorm schema data type of a field. The standard values
// mirror gorm.io/gorm/schema; any other value is a database type given
// through gorm's `type:` tag and is passed to the dialect verbatim.
type DataType string

const (
	Bool   DataType = "bool"
	Int    DataType = "int"
	Uint   DataType = "uint"
	Float  DataType = "float"
	String DataType = "string"
	Time   DataType = "time"
	Bytes  DataType = "bytes"
)

// ReferentialAction is what a foreign key does to the referencing row when
// the referenced row is deleted (OnDelete) or its key changes (OnUpdate):
// Cascade, SetNull, SetDefault, Restrict or NoAction. Its values are spelled
// as the SQL they emit. The zero value omits the clause, so the database
// applies its default (NO ACTION).
type ReferentialAction string

// OnDelete / OnUpdate actions for foreign keys, spelled as SQL.
const (
	Cascade    ReferentialAction = "CASCADE"
	SetNull    ReferentialAction = "SET NULL"
	SetDefault ReferentialAction = "SET DEFAULT"
	Restrict   ReferentialAction = "RESTRICT"
	NoAction   ReferentialAction = "NO ACTION"
)

// Field is the migration-state description of one database column. It plays
// the role of a deconstructed django.db.models.Field: two fields are the same
// for the autodetector exactly when Deconstruct() of both is equal.
//
// The field name inside a ModelState is the database column name (gorm's
// DBName); there is no separate db_column.
type Field struct {
	Type      DataType
	Size      int // bit size for int/uint/float, length for string/bytes
	Precision int
	Scale     int

	PrimaryKey             bool
	AutoIncrement          bool
	AutoIncrementIncrement int64
	Null                   bool
	Unique                 bool

	// DBDefault is the database-level default (gorm's `default:` tag,
	// Django's db_default).
	DBDefault *DBDefault
	// Default is a one-off value used to populate existing rows when the
	// field is added or made NOT NULL (Django's default together with
	// preserve_default=False). It is a literal value or a function with no
	// arguments and a single result; Register refuses an operation whose
	// field carries a function of any other shape.
	Default any

	Comment string

	// Custom is set for fields whose Go type implements
	// gorm.io/gorm/migrator.GormDataTypeInterface. The column type is then
	// obtained from GormDBDataType at migration time.
	Custom *TypeRef

	// Tags carries gorm tag settings that dialects consult when choosing a
	// column type (INDEX, PRECISION, GENERATED, CODEC, TTL). They are passed
	// verbatim to Dialector.DataTypeOf.
	Tags map[string]string

	ForeignKey *ForeignKey

	// State-only attributes; they never produce SQL.
	AutoNowAdd bool // gorm autoCreateTime
	AutoNow    bool // gorm autoUpdateTime
	Serializer string
}

// ForeignKey marks a field as a reference to another model's column.
type ForeignKey struct {
	// To is "app_label.ModelName", or "ModelName" for a model of the same
	// app. A ProjectState stores the qualified form: a field entering a
	// state through AddModel, AddField or AlterField has a bare target
	// rewritten to "app_label.ModelName", so that the two spellings
	// deconstruct equal. The comparison ignores case.
	To string
	// ToField is the referenced field (column) name.
	ToField  string
	OnDelete ReferentialAction
	OnUpdate ReferentialAction
	// Name is the constraint name as gorm names it (fk_<table>_<relation>).
	Name string
}

// DBDefault is a database default: either a literal value or an SQL
// expression.
type DBDefault struct {
	Value any
	Expr  string
}

// DBValue returns a literal database default.
func DBValue(v any) *DBDefault { return &DBDefault{Value: v} }

// DBExpr returns an SQL expression database default, such as
// "CURRENT_TIMESTAMP".
func DBExpr(sql string) *DBDefault { return &DBDefault{Expr: sql} }

// TypeRef identifies a Go type by package path and name so that generated
// migration files can import it. Build it with Custom[T]().
type TypeRef struct {
	Package string
	Name    string
	Type    reflect.Type
}

// Custom returns a TypeRef for T.
func Custom[T any]() *TypeRef {
	t := reflect.TypeFor[T]()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return &TypeRef{Package: t.PkgPath(), Name: t.Name(), Type: t}
}

func (r *TypeRef) String() string {
	if r == nil {
		return ""
	}
	return r.Package + "." + r.Name
}

// IsRelation reports whether the field references another model.
func (f *Field) IsRelation() bool { return f.ForeignKey != nil }

// HasDefault reports whether the field has a one-off default.
func (f *Field) HasDefault() bool { return f.Default != nil }

// Clone returns a deep copy of f.
func (f Field) Clone() Field {
	if f.DBDefault != nil {
		d := *f.DBDefault
		f.DBDefault = &d
	}
	if f.ForeignKey != nil {
		fk := *f.ForeignKey
		f.ForeignKey = &fk
	}
	f.Tags = maps.Clone(f.Tags)
	return f
}

// DefaultValue evaluates the one-off Default. Functions are called, and a
// prompt-entered expression is evaluated when it is a literal (a compiled
// migration holds the real value; only the process that generated the
// migration sees a GoExpr).
func (f *Field) DefaultValue() any {
	if f.Default == nil {
		return nil
	}
	if e, ok := f.Default.(*GoExpr); ok {
		v, ok := e.Literal()
		if !ok {
			return e
		}
		return v
	}
	v := reflect.ValueOf(f.Default)
	if v.Kind() == reflect.Func && v.Type().NumIn() == 0 && v.Type().NumOut() == 1 {
		return v.Call(nil)[0].Interface()
	}
	return f.Default
}

// Equal reports deconstruction equality (the autodetector's notion of
// "unchanged").
func (f Field) Equal(o Field) bool {
	return deconstructEqual(f.Deconstruct(), o.Deconstruct())
}

// The attributes a Field deconstruction can carry that a caller has to
// treat specially. They are the struct field names, named here so that the
// code that branches on one is tied to the code that produces it.
const (
	AttrDefault    = "Default"
	AttrForeignKey = "ForeignKey"
)

// Deconstruct returns the ordered list of non-zero attributes of f. Function
// defaults are compared by their fully-qualified name.
func (f Field) Deconstruct() []KV {
	var kv []KV
	v := reflect.ValueOf(f)
	t := v.Type()
	for i := range t.NumField() {
		fv := v.Field(i)
		if fv.IsZero() {
			continue
		}
		val := fv.Interface()
		switch t.Field(i).Name {
		case AttrDefault:
			val = normalizeDefault(val)
		case AttrForeignKey:
			// The target is compared case-insensitively, so it
			// is always deconstructed in lower case. A Field does
			// not know its app, so it cannot resolve a bare
			// "ModelName": a ProjectState stores the qualified
			// form instead (see qualifyForeignKey).
			fk := *f.ForeignKey
			fk.To = strings.ToLower(fk.To)
			val = &fk
		}
		kv = append(kv, KV{Key: t.Field(i).Name, Value: val})
	}
	return kv
}

// KV is one named argument of a deconstruction: the attribute name and the
// value needed to rebuild the deconstructed value.
type KV struct {
	Key   string
	Value any
}

type funcRef string

// GoExpr is a default value entered at the makemigrations prompt. It only
// exists while writing a migration: the writer emits Source verbatim and the
// compiled migration holds the real value.
type GoExpr struct {
	Source string
}

// String returns the Go source of the expression.
func (e *GoExpr) String() string { return e.Source }

// Literal evaluates the expression when it is a Go literal (nil, a number,
// a string, a rune or a boolean), which is what the prompt usually
// receives. ok is false for anything that needs compiling.
func (e *GoExpr) Literal() (any, bool) {
	src := strings.TrimSpace(e.Source)
	switch src {
	case "nil":
		return nil, true
	case "true":
		return true, true
	case "false":
		return false, true
	case `""`:
		return "", true
	}
	expr, err := parser.ParseExpr(src)
	if err != nil {
		return nil, false
	}
	lit, ok := expr.(*ast.BasicLit)
	if !ok {
		return nil, false
	}
	switch lit.Kind {
	case token.INT:
		v, err := strconv.ParseInt(lit.Value, 0, 64)
		return v, err == nil
	case token.FLOAT:
		v, err := strconv.ParseFloat(lit.Value, 64)
		return v, err == nil
	case token.STRING, token.CHAR:
		v, err := strconv.Unquote(lit.Value)
		return v, err == nil
	}
	return nil, false
}

func normalizeDefault(v any) any {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Func {
		return funcRef(FuncName(v))
	}
	return v
}

// ClassName returns the name of the Django field class this field
// corresponds to. It appears in the makemigrations prompts, such as
// "(a CharField)", which quote Django's wording.
func (f *Field) ClassName() string {
	if f.ForeignKey != nil {
		return "ForeignKey"
	}
	if f.Custom != nil {
		return f.Custom.Name
	}
	switch f.Type {
	case Bool:
		return "BooleanField"
	case Int, Uint:
		prefix := ""
		if f.Type == Uint && !f.AutoIncrement {
			prefix = "Positive"
		}
		if f.AutoIncrement {
			switch {
			case f.Size > 0 && f.Size <= 16:
				return "SmallAutoField"
			case f.Size > 0 && f.Size <= 32:
				return "AutoField"
			}
			return "BigAutoField"
		}
		switch {
		case f.Size > 0 && f.Size <= 16:
			return prefix + "SmallIntegerField"
		case f.Size > 0 && f.Size <= 32:
			return prefix + "IntegerField"
		}
		return prefix + "BigIntegerField"
	case Float:
		if f.Precision > 0 {
			return "DecimalField"
		}
		return "FloatField"
	case String:
		if f.Size > 0 {
			return "CharField"
		}
		return "TextField"
	case Time:
		return "DateTimeField"
	case Bytes:
		return "BinaryField"
	}
	return "Field"
}

func deconstructEqual(a, b []KV) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || !reflect.DeepEqual(a[i].Value, b[i].Value) {
			// TypeRef carries a reflect.Type which DeepEqual compares by
			// identity; compare by name instead.
			ra, oka := a[i].Value.(*TypeRef)
			rb, okb := b[i].Value.(*TypeRef)
			if a[i].Key == b[i].Key && oka && okb && ra.String() == rb.String() {
				continue
			}
			return false
		}
	}
	return true
}

// Target returns the (app_label, model_name_lower) the foreign key points at.
func (fk *ForeignKey) Target(appLabel string) ModelKey {
	return ResolveRelation(fk.To, appLabel)
}

// ModelKey is (app_label, model_name_lower).
type ModelKey struct {
	App   string
	Model string
}

func (k ModelKey) String() string { return k.App + "." + k.Model }

// ResolveRelation turns "app.Model" or "Model" into a ModelKey.
//
// django: migrations/utils.py resolve_relation
func ResolveRelation(ref, appLabel string) ModelKey {
	if app, model, ok := strings.Cut(ref, "."); ok {
		return ModelKey{app, strings.ToLower(model)}
	}
	return ModelKey{appLabel, strings.ToLower(ref)}
}

// NamedField pairs a field name with its definition.
type NamedField struct {
	Name  string
	Field Field
}

// Fields is a model's field list, in declaration order.
type Fields []NamedField

// Get returns the field with the given name.
func (fs Fields) Get(name string) (Field, bool) {
	if i := fs.Index(name); i >= 0 {
		return fs[i].Field, true
	}
	return Field{}, false
}

// Index returns the position of name, or -1.
func (fs Fields) Index(name string) int {
	return slices.IndexFunc(fs, func(f NamedField) bool { return f.Name == name })
}

// Names returns the field names in order.
func (fs Fields) Names() []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Name
	}
	return out
}

func (d *DBDefault) String() string {
	if d == nil {
		return ""
	}
	if d.Expr != "" {
		return d.Expr
	}
	return fmt.Sprint(d.Value)
}
