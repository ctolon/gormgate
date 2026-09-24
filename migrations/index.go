package migrations

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// SortOrder is the direction an index sorts a column in: SortAsc or
// SortDesc. The zero value leaves the order unset, so the database applies
// its default (ascending everywhere gormgate supports).
type SortOrder string

const (
	SortAsc  SortOrder = "ASC"
	SortDesc SortOrder = "DESC"
)

// IndexField is one element of an index: a column (optionally with a sort
// order, collation or prefix length) or an SQL expression.
type IndexField struct {
	Column     string
	Expression string
	Sort       SortOrder
	Collate    string
	Length     int
}

// Columns builds the Fields of a plain multi-column index, so that a
// hand-written migration reads Fields: m.Columns("title", "slug").
func Columns(names ...string) []IndexField {
	out := make([]IndexField, len(names))
	for i, n := range names {
		out[i] = IndexField{Column: n}
	}
	return out
}

// IndexClass is the word that goes before INDEX in a CREATE INDEX
// statement on the backends that have one: gorm's index class.
type IndexClass string

// The index classes gorm recognises. The zero value is an ordinary index.
const (
	FullText IndexClass = "FULLTEXT"
	Spatial  IndexClass = "SPATIAL"
)

// IndexMethod is the access method an index is built with, as it is spelled
// after USING. The set is open: every server has its own, and a backend
// passes the word through.
type IndexMethod string

// The access methods PostgreSQL ships with. Others are spelled out.
const (
	BTree  IndexMethod = "btree"
	Hash   IndexMethod = "hash"
	GIN    IndexMethod = "gin"
	GiST   IndexMethod = "gist"
	SPGiST IndexMethod = "spgist"
	BRIN   IndexMethod = "brin"
)

// Index is a named table index. It covers Django's models.Index and gorm's
// index/uniqueIndex tags (Unique, Class, Type, Option and Comment are gorm
// extensions).
type Index struct {
	Name      string
	Fields    []IndexField
	Unique    bool
	Class     IndexClass  // FULLTEXT, SPATIAL
	Type      IndexMethod // access method: btree, hash, gin, ...
	Where     string      // partial index condition
	Include   []string
	OpClasses []string
	Comment   string
	Option    string // trailing dialect option, e.g. MySQL "WITH PARSER ngram"
}

// Clone returns a deep copy of the index.
func (ix Index) Clone() Index {
	ix.Fields = slices.Clone(ix.Fields)
	ix.Include = slices.Clone(ix.Include)
	ix.OpClasses = slices.Clone(ix.OpClasses)
	return ix
}

// fieldNames returns the index's column names, each prefixed with "-" when
// the column is sorted descending. Expressions are skipped.
func (ix Index) fieldNames() []string {
	var out []string
	for _, f := range ix.Fields {
		if f.Column == "" {
			continue
		}
		if strings.EqualFold(string(f.Sort), string(SortDesc)) {
			out = append(out, "-"+f.Column)
		} else {
			out = append(out, f.Column)
		}
	}
	return out
}

// Columns returns the plain column names referenced by the index.
func (ix Index) Columns() []string {
	var out []string
	for _, f := range ix.Fields {
		if f.Column != "" {
			out = append(out, f.Column)
		}
	}
	return out
}

// HasExpressions reports whether any element is an expression.
func (ix Index) HasExpressions() bool {
	for _, f := range ix.Fields {
		if f.Expression != "" {
			return true
		}
	}
	return false
}

// Equal compares two indexes by value.
func (ix Index) Equal(o Index) bool {
	return deconstructEqual(deconstructStruct(ix), deconstructStruct(o))
}

// describeFor renders the description AddIndex.Describe reports for
// creating ix on the named model.
//
// django: operations/models.py AddIndex.describe
func (ix Index) describeFor(model string) string {
	if ix.HasExpressions() {
		var exprs []string
		for _, f := range ix.Fields {
			if f.Expression != "" {
				exprs = append(exprs, f.Expression)
			} else {
				exprs = append(exprs, f.Column)
			}
		}
		return fmt.Sprintf("create index %s on %s on model %s", ix.Name, strings.Join(exprs, ", "), model)
	}
	return fmt.Sprintf("create index %s on field(s) %s of model %s", ix.Name, strings.Join(ix.fieldNames(), ", "), model)
}

// Constraint is a table-level constraint stored in Options.Constraints.
//
// The interface is sealed by its unexported methods: CheckConstraint,
// UniqueConstraint and ForeignKeyConstraint are the only implementations,
// and a package outside this one cannot add another.
type Constraint interface {
	ConstraintName() string
	cloneConstraint() Constraint
	// constrainedColumns lists the columns the constraint depends on;
	// used for references_field and rename handling.
	constrainedColumns() []string
	renameColumn(old, new string) Constraint
}

// CheckConstraint is a CHECK constraint.
type CheckConstraint struct {
	Name  string
	Check string
	// ViolationErrorMessage is state-only (Django's
	// violation_error_message); changing it alone is an AlterConstraint.
	ViolationErrorMessage string
}

func (c *CheckConstraint) ConstraintName() string { return c.Name }
func (c *CheckConstraint) cloneConstraint() Constraint {
	cp := *c
	return &cp
}
func (c *CheckConstraint) constrainedColumns() []string { return nil }
func (c *CheckConstraint) renameColumn(string, string) Constraint {
	return c.cloneConstraint()
}

// Deferrable is when a deferrable constraint is checked: Deferred (at the
// end of the transaction) or Immediate (after every statement, but still
// switchable with SET CONSTRAINTS). The zero value makes the constraint not
// deferrable at all, which is the database default.
type Deferrable string

const (
	Deferred  Deferrable = "DEFERRED"
	Immediate Deferrable = "IMMEDIATE"
)

// UniqueConstraint is a multi-column unique constraint, optionally partial
// (Condition), deferrable or covering (Include).
type UniqueConstraint struct {
	Name          string
	Fields        []string
	Condition     string
	Deferrable    Deferrable
	Include       []string
	NullsDistinct *bool
	// ViolationErrorMessage is state-only.
	ViolationErrorMessage string
}

func (c *UniqueConstraint) ConstraintName() string { return c.Name }
func (c *UniqueConstraint) cloneConstraint() Constraint {
	cp := *c
	cp.Fields = slices.Clone(c.Fields)
	cp.Include = slices.Clone(c.Include)
	if c.NullsDistinct != nil {
		v := *c.NullsDistinct
		cp.NullsDistinct = &v
	}
	return &cp
}
func (c *UniqueConstraint) constrainedColumns() []string {
	return append(slices.Clone(c.Fields), c.Include...)
}
func (c *UniqueConstraint) renameColumn(old, new string) Constraint {
	cp := c.cloneConstraint().(*UniqueConstraint)
	for i, f := range cp.Fields {
		if f == old {
			cp.Fields[i] = new
		}
	}
	for i, f := range cp.Include {
		if f == old {
			cp.Include[i] = new
		}
	}
	return cp
}

// ForeignKeyConstraint is a foreign key spanning one or more columns,
// held as a table-level constraint.
//
// Django has no counterpart to port: its ForeignKey is always a single
// field, and a composite foreign key cannot be expressed in its model
// layer at all. gorm's can be, with `foreignKey:A,B;references:X,Y`, and a
// key over several columns does not fit on one Field, so gormgate keeps it
// beside the unique and check constraints instead. A single-column
// relation stays on its field (Field.ForeignKey); this kind exists for the
// keys a field cannot hold.
type ForeignKeyConstraint struct {
	// Name is the constraint name, as gorm names it
	// (fk_<table>_<relation>).
	Name string
	// Fields are the constrained columns of this model, in the order they
	// appear in the FOREIGN KEY clause.
	Fields []string
	// To is "app_label.ModelName", or "ModelName" for a model of the same
	// app, exactly as ForeignKey.To. A ProjectState stores the qualified
	// form.
	To string
	// ToFields are the referenced columns of To. They line up with Fields
	// one by one, so the two slices always have the same length.
	ToFields []string
	OnDelete ReferentialAction
	OnUpdate ReferentialAction
}

func (c *ForeignKeyConstraint) ConstraintName() string { return c.Name }
func (c *ForeignKeyConstraint) cloneConstraint() Constraint {
	cp := *c
	cp.Fields = slices.Clone(c.Fields)
	cp.ToFields = slices.Clone(c.ToFields)
	return &cp
}
func (c *ForeignKeyConstraint) constrainedColumns() []string { return slices.Clone(c.Fields) }
func (c *ForeignKeyConstraint) renameColumn(old, new string) Constraint {
	cp := c.cloneConstraint().(*ForeignKeyConstraint)
	cp.Fields = renameIn(cp.Fields, old, new)
	return cp
}

// Target returns the (app_label, model_name_lower) the constraint points at.
func (c *ForeignKeyConstraint) Target(appLabel string) ModelKey {
	return ResolveRelation(c.To, appLabel)
}

// renameTargetColumn returns a copy with the referenced column old spelled
// new. It is used when the field it points at is renamed.
func (c *ForeignKeyConstraint) renameTargetColumn(old, new string) *ForeignKeyConstraint {
	cp := c.cloneConstraint().(*ForeignKeyConstraint)
	cp.ToFields = renameIn(cp.ToFields, old, new)
	return cp
}

// ConstraintEqual compares constraints by value.
func ConstraintEqual(a, b Constraint) bool {
	if reflect.TypeOf(a) != reflect.TypeOf(b) {
		return false
	}
	return deconstructEqual(deconstructStruct(a), deconstructStruct(b))
}

// ConstraintEqualIgnoringNonDB compares constraints ignoring state-only
// attributes; it is the test the autodetector uses to decide whether a
// constraint has to be dropped and recreated.
//
// django: autodetector.py MigrationAutodetector._constraint_should_be_dropped_and_recreated
func ConstraintEqualIgnoringNonDB(a, b Constraint) bool {
	strip := func(c Constraint) Constraint {
		c = c.cloneConstraint()
		switch v := c.(type) {
		case *CheckConstraint:
			v.ViolationErrorMessage = ""
		case *UniqueConstraint:
			v.ViolationErrorMessage = ""
		case *ForeignKeyConstraint:
			// Every attribute of a foreign key produces SQL, so there is
			// nothing state-only to strip.
		}
		return c
	}
	return ConstraintEqual(strip(a), strip(b))
}
