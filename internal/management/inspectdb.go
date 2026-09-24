package management

import (
	"fmt"
	"go/format"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/ctolon/gormgate/backends/base"
)

// inspectOptions are inspectdb's options.
type inspectOptions struct {
	tables            []string
	includePartitions bool
	includeViews      bool
}

// newInspectDBCmd builds the inspectdb command. It reads the database
// rather than the models, so it does not run the system checks.
func newInspectDBCmd(s *state) *cobra.Command {
	var (
		database string
		o        inspectOptions
	)
	cmd := &cobra.Command{
		Use:   "inspectdb [table ...]",
		Short: "print gorm model structs for the tables already in a database",
		Long: "Introspect a database and write model structs matching what is there,\n" +
			"as a starting point for adopting an existing schema.",
		Args: usageArgs(cobra.ArbitraryArgs),
		RunE: func(c *cobra.Command, args []string) error {
			if err := s.checkDatabase(database); err != nil {
				return err
			}
			o.tables = args
			return inspectDB(s.ctx, database, o)
		},
	}
	databaseFlag(cmd, &database)
	cmd.Flags().BoolVar(&o.includePartitions, "include-partitions", false, "also write models for partition tables")
	cmd.Flags().BoolVar(&o.includeViews, "include-views", false, "also write models for views")
	return cmd
}

// GoTyper lets a backend map introspected column types to Go types; the
// generic mapping is used when a backend doesn't implement it.
type GoTyper interface {
	// GoTypeOf returns the Go type, extra gorm tag settings and notes for
	// a column.
	GoTypeOf(c base.ColumnInfo) (goType string, tags []string, notes []string)
}

// inspectDB introspects a database and prints model structs for it.
func inspectDB(ctx *Context, alias string, o inspectOptions) error {
	conn, err := ctx.Project.Conn(alias)
	if err != nil {
		return err
	}
	intro := conn.Introspection()
	var body strings.Builder
	needs := map[string]bool{}
	out := lineWriter{b: &body}
	out.Line("// This is an auto-generated gormgate model file.")
	out.Line("// You'll have to do the following manually to clean this up:")
	out.Line("//   * Rearrange models' order")
	out.Line("//   * Make sure each model has one field with primaryKey")
	out.Line("//   * Make sure each relation field has the constraint settings you want")
	out.Line("//   * Remove `MigrationMeta` with Managed=false if you wish gormgate to create, modify and delete the table")
	out.Line("// Feel free to rename the models, but don't rename the table names or field columns.")
	types := map[string]bool{"t": true}
	if o.includePartitions {
		types["p"] = true
	}
	if o.includeViews {
		types["v"], types["m"] = true, true
	}
	all, err := intro.TableNames(true)
	if err != nil {
		return err
	}
	info := map[string]base.TableInfo{}
	var names []string
	for _, t := range all {
		if !types[t.Type] {
			continue
		}
		info[t.Name] = t
		names = append(names, t.Name)
	}
	slices.Sort(names)
	if len(o.tables) > 0 {
		names = o.tables
	}
	// The backend's Go type mapping, if it has one, does not change from
	// column to column.
	typer, _ := intro.(GoTyper)
	var knownModels []string
	for _, table := range names {
		// Django's introspection raises NotImplementedError where a
		// backend cannot answer; gormgate's backends answer with an empty
		// result instead, so every error here is a real failure and lands
		// in Django's "Unable to inspect table" branch.
		relations, constraints, pk, columns, err := describeTable(intro, table)
		if err != nil {
			out.Line(fmt.Sprintf("// Unable to inspect table '%s'", table))
			out.Line(fmt.Sprintf("// The error was: %s", err))
			continue
		}
		unique := map[string]bool{}
		for _, c := range constraints {
			if c.Unique && len(c.Columns) == 1 {
				unique[c.Columns[0]] = true
			}
		}
		modelName := normalizeTableName(table)
		knownModels = append(knownModels, modelName)
		out.Line("")
		out.Line(fmt.Sprintf("type %s struct {", modelName))
		usedNames := map[string]bool{}
		for _, col := range columns {
			var tags, notes []string
			fieldName := normalizeColName(col.Name, usedNames)
			usedNames[fieldName] = true
			if toDBName(fieldName) != col.Name {
				tags = append(tags, "column:"+col.Name)
			}
			goType, typeTags, typeNotes := goTypeOf(typer, col)
			tags = append(tags, typeTags...)
			notes = append(notes, typeNotes...)
			if slices.Contains(pk, col.Name) {
				tags = append(tags, "primaryKey")
			} else if unique[col.Name] {
				tags = append(tags, "unique")
			}
			if col.AutoIncrement {
				tags = append(tags, "autoIncrement")
			}
			if !col.Null {
				tags = append(tags, "not null")
			} else if !strings.HasPrefix(goType, "*") && goType != "[]byte" {
				goType = "*" + goType
			}
			if col.Default != nil && *col.Default != "" && !col.AutoIncrement {
				tags = append(tags, "default:"+*col.Default)
			}
			if col.Comment != "" {
				tags = append(tags, "comment:"+col.Comment)
			}
			if rel, ok := relations[col.Name]; ok {
				target := normalizeTableName(rel.ToTable)
				notes = append(notes, fmt.Sprintf("references %s(%s)", rel.ToTable, rel.ToColumn))
				if !slices.Contains(knownModels, target) {
					notes = append(notes, fmt.Sprintf("model %s is defined below", target))
				}
			}
			if strings.HasPrefix(strings.TrimPrefix(goType, "*"), "time.") {
				needs["time"] = true
			}
			line := fmt.Sprintf("\t%s %s", fieldName, goType)
			if len(tags) > 0 {
				line += fmt.Sprintf(" `gorm:%q`", strings.Join(tags, ";"))
			}
			if len(notes) > 0 {
				line += " // " + strings.Join(notes, " ")
			}
			out.Line(line)
		}
		out.Line("}")
		out.Line("")
		out.Line(fmt.Sprintf("func (%s) TableName() string { return %q }", modelName, table))
		meta := metaFor(conn, table, info[table], constraints)
		needs["gormgate"] = true
		for _, line := range meta {
			out.Line(line)
		}
	}
	var head strings.Builder
	head.WriteString("package models\n\n")
	if needs["time"] || needs["gormgate"] {
		head.WriteString("import (\n")
		if needs["time"] {
			head.WriteString("\t\"time\"\n\n")
		}
		if needs["gormgate"] {
			head.WriteString("\tm \"github.com/ctolon/gormgate/migrations\"\n")
		}
		head.WriteString(")\n")
	}
	text := body.String()
	i := strings.Index(text, "\n\n")
	comments, rest := text, ""
	if i >= 0 {
		comments, rest = text[:i+1], text[i+2:]
	}
	source := comments + "\n" + head.String() + "\n" + rest
	if formatted, err := format.Source([]byte(source)); err == nil {
		source = string(formatted)
	}
	ctx.Stdout.Print(source)
	return nil
}

// lineWriter collects generated lines of Go source.
type lineWriter struct{ b *strings.Builder }

// Line appends one line.
func (w lineWriter) Line(line string) {
	w.b.WriteString(line)
	w.b.WriteString("\n")
}

// metaFor renders the MigrationMeta method with the options inspectdb can
// recover (unmanaged tables, table comment, multi-column unique
// constraints and indexes).
func metaFor(conn *base.Conn, table string, info base.TableInfo, constraints map[string]base.ConstraintInfo) []string {
	var uniques, indexes []string
	for _, name := range sortedMapKeys(constraints) {
		c := constraints[name]
		if len(c.Columns) < 2 || c.PrimaryKey || c.ForeignKey != nil {
			continue
		}
		cols := "\"" + strings.Join(c.Columns, "\", \"") + "\""
		switch {
		case c.Unique:
			uniques = append(uniques, fmt.Sprintf("\t\t\t{%s},", cols))
		case c.Index:
			indexes = append(indexes, fmt.Sprintf("\t\t\t{Name: %q, Fields: []m.IndexField{%s}},", name, indexFields(c.Columns)))
		}
	}
	var out []string
	out = append(out, "")
	out = append(out, fmt.Sprintf("func (%s) MigrationMeta() m.Meta {", normalizeTableName(table)))
	out = append(out, "\treturn m.Meta{")
	out = append(out, "\t\tManaged: m.Ptr(false),")
	if conn.Backend.Features.SupportsComments && info.Comment != "" {
		out = append(out, fmt.Sprintf("\t\tDBTableComment: %q,", info.Comment))
	}
	if len(uniques) > 0 {
		out = append(out, "\t\tUniqueTogether: [][]string{")
		out = append(out, uniques...)
		out = append(out, "\t\t},")
	}
	if len(indexes) > 0 {
		out = append(out, "\t\tIndexes: []m.Index{")
		out = append(out, indexes...)
		out = append(out, "\t\t},")
	}
	out = append(out, "\t}")
	out = append(out, "}")
	return out
}

func indexFields(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = fmt.Sprintf("{Column: %q}", c)
	}
	return strings.Join(parts, ", ")
}

var (
	nonIdent    = regexp.MustCompile(`[^A-Za-z0-9_]`)
	underscores = regexp.MustCompile(`_+`)
)

// normalizeTableName turns a table name into a Go type name.
func normalizeTableName(table string) string {
	clean := underscores.ReplaceAllString(nonIdent.ReplaceAllString(table, "_"), "_")
	var b strings.Builder
	for _, part := range strings.Split(clean, "_") {
		if part == "" {
			continue
		}
		r := []rune(part)
		b.WriteString(strings.ToUpper(string(r[0])) + string(r[1:]))
	}
	name := b.String()
	if name == "" || unicode.IsDigit(rune(name[0])) {
		name = "Model" + name
	}
	return name
}

// normalizeColName turns a column name into a Go field name, appending a
// number on conflicts.
func normalizeColName(col string, used map[string]bool) string {
	name := normalizeTableName(col)
	if name == "" {
		name = "Field"
	}
	if !used[name] {
		return name
	}
	for i := 1; ; i++ {
		candidate := name + strconv.Itoa(i)
		if !used[candidate] {
			return candidate
		}
	}
}

// toDBName is gorm's default column name for a Go field name.
func toDBName(name string) string {
	var b strings.Builder
	runes := []rune(name)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if i > 0 && (!unicode.IsUpper(runes[i-1]) || (i+1 < len(runes) && !unicode.IsUpper(runes[i+1]))) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

var typeSize = regexp.MustCompile(`\((\d+)(?:,\s*(\d+))?\)`)

// goTypeOf maps an introspected column type to a Go type and gorm tags.
func goTypeOf(typer GoTyper, c base.ColumnInfo) (string, []string, []string) {
	if typer != nil {
		if goType, tags, notes := typer.GoTypeOf(c); goType != "" {
			return goType, tags, notes
		}
	}
	base := strings.ToLower(strings.TrimSpace(typeSize.ReplaceAllString(c.Type, "")))
	var tags, notes []string
	size, precision, scale := c.Size, c.Precision, c.Scale
	if mm := typeSize.FindStringSubmatch(c.Type); mm != nil && size == 0 && precision == 0 {
		n, _ := strconv.Atoi(mm[1])
		if strings.Contains(base, "numeric") || strings.Contains(base, "decimal") {
			precision = n
			scale, _ = strconv.Atoi(mm[2])
		} else {
			size = n
		}
	}
	switch {
	case strings.Contains(base, "bool") || base == "bit":
		return "bool", tags, notes
	case strings.HasPrefix(base, "tinyint"), base == "smallint", base == "int2":
		return "int16", tags, notes
	case base == "integer", base == "int", base == "int4", base == "mediumint", base == "serial":
		return "int32", tags, notes
	case base == "bigint", base == "int8", base == "bigserial":
		return "int64", tags, notes
	case strings.Contains(base, "numeric"), strings.Contains(base, "decimal"), base == "number":
		if precision > 0 {
			tags = append(tags, "precision:"+strconv.Itoa(precision))
			if scale > 0 {
				tags = append(tags, "scale:"+strconv.Itoa(scale))
			}
		}
		notes = append(notes, "This field type is a guess.")
		return "float64", tags, notes
	case strings.Contains(base, "double"), base == "real", base == "float", strings.HasPrefix(base, "float"):
		return "float64", tags, notes
	case strings.Contains(base, "char"), base == "citext":
		if size > 0 {
			tags = append(tags, "size:"+strconv.Itoa(size))
		}
		return "string", tags, notes
	case strings.Contains(base, "text"), strings.Contains(base, "clob"):
		tags = append(tags, "type:"+c.Type)
		return "string", tags, notes
	case strings.Contains(base, "timestamp"), strings.Contains(base, "datetime"), base == "date", base == "time":
		if base == "date" || base == "time" {
			tags = append(tags, "type:"+c.Type)
		}
		return "time.Time", tags, notes
	case strings.Contains(base, "blob"), strings.Contains(base, "binary"), base == "bytea", base == "bytes":
		return "[]byte", tags, notes
	case base == "uuid":
		tags = append(tags, "type:uuid")
		return "string", tags, notes
	case strings.Contains(base, "json"):
		tags = append(tags, "type:"+c.Type)
		notes = append(notes, "This field type is a guess.")
		return "string", tags, notes
	}
	tags = append(tags, "type:"+c.Type)
	notes = append(notes, "This field type is a guess.")
	return "string", tags, notes
}

// describeTable collects everything inspectdb needs about one table.  Any
// failure aborts the table, which is Django's outer "except Exception".
func describeTable(intro base.Introspection, table string) (map[string]base.RelationInfo, map[string]base.ConstraintInfo, []string, []base.ColumnInfo, error) {
	relations, err := intro.Relations(table)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	constraints, err := intro.Constraints(table)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	pk, err := intro.PrimaryKeyColumns(table)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	columns, err := intro.TableDescription(table)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return relations, constraints, pk, columns, nil
}
