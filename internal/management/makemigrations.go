package management

import (
	"errors"
	"fmt"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctolon/gormgate/internal/autodetector"
	"github.com/ctolon/gormgate/internal/graph"
	"github.com/ctolon/gormgate/internal/loader"
	"github.com/ctolon/gormgate/internal/optimizer"
	"github.com/ctolon/gormgate/internal/questioner"
	"github.com/ctolon/gormgate/internal/writer"
	m "github.com/ctolon/gormgate/migrations"
)

// makeMigrationsOptions are makemigrations' options.
type makeMigrationsOptions struct {
	appLabels   []string
	dryRun      bool
	merge       bool
	empty       bool
	interactive bool
	name        string
	header      bool
	// check exits non-zero when a migration is missing. It implies dryRun.
	check      bool
	scriptable bool
	update     bool
}

// makeMigrations is one run of makemigrations.
type makeMigrations struct {
	ctx *Context
	o   makeMigrationsOptions
	// dryRun is o.dryRun, which --check also turns on.
	dryRun       bool
	writtenFiles []string
}

// newMakeMigrationsCmd builds the makemigrations command.
func newMakeMigrationsCmd(s *state) *cobra.Command {
	var (
		noInput  bool
		noHeader bool
		o        makeMigrationsOptions
	)
	cmd := &cobra.Command{
		Use:   "makemigrations [app ...]",
		Short: "write migrations for the changes made to the model structs",
		Long: "Compare the model structs with the migrations already written and\n" +
			"write new migrations for whatever differs.",
		Args: usageArgs(cobra.ArbitraryArgs),
		RunE: func(c *cobra.Command, args []string) error {
			if err := s.runChecks(); err != nil {
				return err
			}
			o.appLabels = args
			o.interactive = !noInput
			o.header = !noHeader
			return (&makeMigrations{ctx: s.ctx, o: o}).run()
		},
	}
	f := cmd.Flags()
	f.BoolVar(&o.dryRun, "dry-run", false, "show what would be written, and write nothing")
	f.BoolVar(&o.merge, "merge", false, "write a migration that merges conflicting leaves")
	f.BoolVar(&o.empty, "empty", false, "write an empty migration to fill in by hand")
	f.BoolVarP(&noInput, "no-input", "y", false, "do not prompt; assume the answer that carries on")
	f.StringVarP(&o.name, "name", "n", "", "name for the new migration")
	f.BoolVar(&noHeader, "no-header", false, "omit the generated-by comment at the top of the file")
	f.BoolVar(&o.check, "check", false, "exit non-zero if a migration is missing, and write nothing")
	f.BoolVar(&o.scriptable, "scriptable", false, "write only the generated paths on stdout, everything else on stderr")
	f.BoolVar(&o.update, "update", false, "fold the changes into the latest migration instead of writing a new one")
	return cmd
}

// logStream is where progress goes: --scriptable diverts it to stderr so
// that stdout carries only the paths of the generated files.
func (c *makeMigrations) logStream() *Stream {
	if c.o.scriptable {
		return c.ctx.Stderr
	}
	return c.ctx.Stdout
}

func (c *makeMigrations) log(msg string) { c.logStream().Println(msg) }

// isIdentifier reports whether s can name a migration function.
func isIdentifier(s string) bool {
	return token.IsIdentifier(s) || (s != "" && token.Lookup(s).IsKeyword())
}

// run writes the migrations for whatever changed.
func (c *makeMigrations) run() error {
	ctx := c.ctx
	c.writtenFiles = nil
	if c.o.name != "" && !isIdentifier(c.o.name) {
		return Errorf("the migration name must be a valid Go identifier")
	}
	// --check implies --dry-run.
	c.dryRun = c.o.dryRun || c.o.check
	if c.o.scriptable {
		ctx.Stderr.SetStyle(nil)
	}
	labels := uniqueSorted(c.o.appLabels)
	bad := false
	for _, l := range labels {
		if ctx.Project.App(l) == nil {
			ctx.Stderr.Println(fmt.Sprintf("No installed app with label '%s'.", l))
			bad = true
		}
	}
	if bad {
		return Silent(ExitUsage, "an app named on the command line is not in the project")
	}
	l, err := ctx.Project.Loader(nil, true)
	if err != nil {
		return err
	}
	// Raise an error if any migrations are applied before their
	// dependencies (default database; all databases when routers are
	// configured).
	aliases := []string{"default"}
	if len(ctx.Project.Routers) > 0 {
		aliases = ctx.Project.DatabaseAliases()
		slices.Sort(aliases)
	}
	for _, alias := range aliases {
		if _, ok := ctx.Project.Databases[alias]; !ok {
			continue
		}
		conn, err := ctx.Project.Conn(alias)
		if err == nil {
			if !anyModelAllowed(ctx.Project, conn.AllowMigrate) {
				continue
			}
			err = l.CheckConsistentHistory(newRecorder(conn), alias)
			var ih *m.InconsistentMigrationHistory
			if errors.As(err, &ih) {
				return err
			}
		}
		if err != nil {
			ctx.Stderr.Println(fmt.Sprintf("warning: could not check the migration history on %s: %s", alias, err))
		}
	}
	conflicts := l.DetectConflicts()
	if len(labels) > 0 {
		for app := range conflicts {
			if !slices.Contains(labels, app) {
				delete(conflicts, app)
			}
		}
	}
	if len(conflicts) > 0 && !c.o.merge {
		var parts []string
		for _, app := range sortedMapKeys(conflicts) {
			parts = append(parts, fmt.Sprintf("%s in %s", strings.Join(conflicts[app], ", "), app))
		}
		return Errorf("conflicting migrations detected; multiple leaf nodes in the migration graph: (%s).\nTo fix them run '%s makemigrations --merge'", strings.Join(parts, "; "), ctx.Invocation())
	}
	if c.o.merge && len(conflicts) == 0 {
		c.log("no conflicts to merge")
		return nil
	}
	if c.o.merge {
		return c.handleMerge(l, conflicts)
	}
	specified := map[string]bool{}
	for _, lbl := range labels {
		specified[lbl] = true
	}
	to, err := ctx.Project.CurrentState()
	if err != nil {
		return err
	}
	base := questioner.Base{SpecifiedApps: specified, DryRun: c.dryRun, Dirs: ctx.Project.MigrationsDir}
	var q questioner.Questioner
	if c.o.interactive {
		iq := questioner.NewInteractive(base, c.logStream().Writer(), ctx.Stdin)
		iq.FieldLookup = func(model, field string) *m.Field {
			for _, ms := range to.Models {
				if ms.NameLower() == strings.ToLower(model) {
					if f, ok := ms.Fields.Get(field); ok {
						return &f
					}
				}
			}
			return nil
		}
		q = iq
	} else {
		q = &questioner.NonInteractive{Base: base, Verbosity: c.ctx.Verbosity, Log: c.log}
	}
	realModels, err := ctx.Project.RealModels(l.UnmigratedApps)
	if err != nil {
		return err
	}
	from, err := l.ProjectState(nil, true, realModels)
	if err != nil {
		return err
	}
	ad := autodetector.New(from, to, q)
	if c.o.empty {
		if len(labels) == 0 {
			return Errorf("you must supply at least one app label when using --empty")
		}
		changes := map[string][]*m.Migration{}
		for _, app := range labels {
			changes[app] = []*m.Migration{{App: app, Name: "custom"}}
		}
		if changes, err = ad.ArrangeForGraph(changes, l.Graph, c.o.name); err != nil {
			return err
		}
		return c.writeMigrationFiles(changes, sortedMapKeys(changes), nil)
	}
	changes, err := ad.ChangesFor(l.Graph, autodetector.ChangesOptions{
		TrimToApps: labels, ConvertApps: labels, MigrationName: c.o.name,
	})
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		if c.ctx.Verbosity >= 1 {
			switch len(labels) {
			case 0:
				c.log("no changes detected")
			case 1:
				c.log(fmt.Sprintf("no changes detected in %s", labels[0]))
			default:
				c.log(fmt.Sprintf("no changes detected in %s", strings.Join(labels, ", ")))
			}
		}
		return nil
	}
	order := ad.Order(changes)
	if c.o.update {
		err = c.writeToLastMigrationFiles(changes, order)
	} else {
		err = c.writeMigrationFiles(changes, order, nil)
	}
	if err != nil {
		return err
	}
	if c.o.check {
		return Silent(ExitError, "changes are missing a migration")
	}
	return nil
}

// anyModelAllowed reports whether the routers let any model at all onto a
// connection. It is asked before the migration history is checked, so it
// works from the declared model structs rather than from a project state.
func anyModelAllowed(p *Project, allow func(app string, h m.Hints) bool) bool {
	for _, a := range p.Apps {
		for _, model := range a.Models {
			name := fmt.Sprintf("%T", model)
			if i := strings.LastIndex(name, "."); i >= 0 {
				name = name[i+1:]
			}
			if allow(a.Label, m.Hints{ModelName: strings.ToLower(name)}) {
				return true
			}
		}
	}
	return false
}

func uniqueSorted(xs []string) []string {
	out := slices.Clone(xs)
	slices.Sort(out)
	return slices.Compact(out)
}

func sortedMapKeys[V any](mp map[string]V) []string {
	return slices.Sorted(maps.Keys(mp))
}

// migrationWriter builds a writer for mig in its app's package.
func migrationWriter(p *Project, mig *m.Migration, header bool) (*writer.Writer, string, error) {
	app := p.App(mig.App)
	if app == nil {
		return nil, "", &m.ValueError{Msg: fmt.Sprintf("no app named %q", mig.App)}
	}
	if app.Disabled || app.MigrationsDir == "" {
		return nil, "", &m.ValueError{Msg: fmt.Sprintf("gormgate can't create migrations for app '%s' because migrations have been disabled via the MigrationModules setting.", mig.App)}
	}
	w := &writer.Writer{Migration: mig, IncludeHeader: header, PackageName: packageNameOf(app.MigrationsDir), PackagePath: app.MigrationsImport}
	return w, filepath.Join(app.MigrationsDir, writer.Filename(mig.Name)), nil
}

func packageNameOf(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "migrations"
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
				files[e.Name()] = b
			}
		}
	}
	if name := writer.PackageNameOf(files); name != "" {
		return name
	}
	return "migrations"
}

// ensurePackage creates the migrations package (migrations.go registering
// the directory) and links it into the gorm-gate command, Django's creation
// of the migrations package with __init__.py.
func ensurePackage(p *Project, app *App) error {
	if err := os.MkdirAll(app.MigrationsDir, 0o755); err != nil {
		return err
	}
	pkgFile := filepath.Join(app.MigrationsDir, "migrations.go")
	if _, err := os.Stat(pkgFile); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(pkgFile, writer.PackageFile(packageNameOf(app.MigrationsDir), app.Label), 0o644); err != nil {
			return err
		}
	}
	return writeLinkFile(p)
}

// LinkFileName is the file in the gorm-gate command's package that imports
// every migrations package.
const LinkFileName = "zz_gormgate_migrations.go"

func writeLinkFile(p *Project) error {
	if p.CommandDir == "" {
		return nil
	}
	var imports []string
	for _, a := range p.Apps {
		if a.Disabled || a.MigrationsImport == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(a.MigrationsDir, "migrations.go")); err == nil {
			imports = append(imports, a.MigrationsImport)
		}
	}
	pkg := p.CommandPackage
	if pkg == "" {
		pkg = "main"
	}
	path := filepath.Join(p.CommandDir, LinkFileName)
	content := writer.LinkFile(pkg, imports)
	if old, err := os.ReadFile(path); err == nil && string(old) == string(content) {
		return nil
	}
	return os.WriteFile(path, content, 0o644)
}

// writeMigrationFiles writes (or, in dry-run, describes) migrations.
func (c *makeMigrations) writeMigrationFiles(changes map[string][]*m.Migration, order []string, previous map[string]string) error {
	created := map[string]bool{}
	for _, app := range order {
		for _, mig := range changes[app] {
			w, path, err := migrationWriter(c.ctx.Project, mig, c.o.header)
			if err != nil {
				return err
			}
			rel := relPath(c.ctx.Env, path)
			if c.ctx.Verbosity >= 1 {
				c.log(c.ctx.Style.MigrateHeading("created ") + c.ctx.Style.MigrateLabel(rel))
				for _, op := range mig.Operations {
					c.log("  " + m.FormattedDescription(op))
				}
				if c.o.scriptable {
					c.ctx.Stdout.Println(rel)
				}
			}
			if !c.dryRun {
				if !created[app] {
					if err := ensurePackage(c.ctx.Project, c.ctx.Project.App(app)); err != nil {
						return err
					}
					created[app] = true
				}
				content, err := w.AsString()
				if err != nil {
					return err
				}
				if err := os.WriteFile(path, content, 0o644); err != nil {
					return err
				}
				c.writtenFiles = append(c.writtenFiles, path)
				if prev, ok := previous[app]; ok {
					relPrev := relPath(c.ctx.Env, prev)
					if w.NeedsManualPorting {
						c.log(c.ctx.Style.Warning(fmt.Sprintf("Updated migration %s requires manual porting.\nPrevious migration %s was kept and must be deleted after porting functions manually.", rel, relPrev)))
					} else {
						if err := os.Remove(prev); err != nil {
							return err
						}
						c.log("deleted " + relPrev)
					}
				}
			} else if c.ctx.Verbosity == 3 {
				content, err := w.AsString()
				if err != nil {
					return err
				}
				c.log(c.ctx.Style.MigrateHeading(fmt.Sprintf("full contents of %s:", filepath.Base(path))))
				c.log(string(content))
			}
		}
	}
	return nil
}

// writeToLastMigrationFiles merges changes into the latest migration.
func (c *makeMigrations) writeToLastMigrationFiles(changes map[string][]*m.Migration, order []string) error {
	l, err := loader.New(c.ctx.Project.LoaderConfig(c.defaultConn(), false))
	if err != nil {
		return err
	}
	newChanges := map[string][]*m.Migration{}
	previous := map[string]string{}
	for _, app := range order {
		leaves := l.Graph.LeafNodes(app)
		if len(leaves) == 0 {
			return Errorf("app %s has no migration, cannot update last migration", app)
		}
		leaf := l.Graph.Nodes[leaves[0]].Clone()
		if len(leaf.Replaces) > 0 {
			return Errorf("cannot update squash migration '%s'", leaf)
		}
		if l.Applied[leaves[0]] {
			return Errorf("cannot update applied migration '%s'", leaf)
		}
		var depending []string
		for _, k := range diskKeys(l) {
			for _, d := range l.DiskMigrations[k].Dependencies {
				if d == leaves[0] {
					depending = append(depending, "'"+l.DiskMigrations[k].String()+"'")
				}
			}
		}
		if len(depending) > 0 {
			return Errorf("cannot update migration '%s' that migrations %s depend on", leaf, strings.Join(depending, ", "))
		}
		for _, mig := range changes[app] {
			leaf.Operations = append(leaf.Operations, mig.Operations...)
			for _, d := range mig.Dependencies {
				if d.App != mig.App {
					leaf.Dependencies = append(leaf.Dependencies, d)
				}
			}
		}
		leaf.Operations = optimizer.Optimize(leaf.Operations, app)
		_, prevPath, err := migrationWriter(c.ctx.Project, l.Graph.Nodes[leaves[0]], true)
		if err != nil {
			return err
		}
		fragment := c.o.name
		if fragment == "" {
			fragment = leaf.SuggestName()
		}
		suggested := leaf.Name[:4] + "_" + fragment
		if leaf.Name == suggested {
			leaf.Name += "_updated"
		} else {
			leaf.Name = suggested
		}
		newChanges[app] = []*m.Migration{leaf}
		previous[app] = prevPath
	}
	return c.writeMigrationFiles(newChanges, order, previous)
}

func (c *makeMigrations) defaultConn() loader.Applied {
	conn, err := c.ctx.Project.Conn("default")
	if err != nil {
		return nil
	}
	return newRecorder(conn)
}

// diskKeys returns the keys of the migrations found on disk, in the
// (app, name) order the graph sorts by.
func diskKeys(l *loader.Loader) []m.Key {
	keys := slices.Collect(maps.Keys(l.DiskMigrations))
	graph.SortKeys(keys)
	return keys
}

// handleMerge creates merge migrations for conflicting leaves.
func (c *makeMigrations) handleMerge(l *loader.Loader, conflicts map[string][]string) error {
	var q questioner.Questioner
	if c.o.interactive {
		q = questioner.NewInteractive(questioner.Base{}, c.logStream().Writer(), c.ctx.Stdin)
	} else {
		q = &questioner.Base{Defaults: questioner.Defaults{Merge: true}}
	}
	for _, app := range sortedMapKeys(conflicts) {
		type branch struct {
			mig      *m.Migration
			ancestry []m.Key
			ops      []m.Operation
		}
		var merges []*branch
		for _, name := range conflicts[app] {
			mig, _ := l.GetMigration(app, name)
			fp, err := l.Graph.ForwardsPlan(m.Key{App: app, Name: name})
			if err != nil {
				return err
			}
			b := &branch{mig: mig}
			for _, k := range fp {
				if k.App == app {
					b.ancestry = append(b.ancestry, k)
				}
			}
			merges = append(merges, b)
		}
		common := 0
		for {
			ok := true
			for _, b := range merges {
				if common >= len(b.ancestry) || b.ancestry[common] != merges[0].ancestry[common] {
					ok = false
					break
				}
			}
			if !ok {
				break
			}
			common++
		}
		if common == 0 {
			return &m.ValueError{Msg: fmt.Sprintf("could not find common ancestor of %s", pythonListRepr(conflicts[app]))}
		}
		for _, b := range merges {
			for _, k := range b.ancestry[common:] {
				mig, _ := l.GetMigration(k.App, k.Name)
				b.ops = append(b.ops, mig.Operations...)
			}
		}
		if c.ctx.Verbosity > 0 {
			c.log(c.ctx.Style.MigrateHeading("merging " + app))
			for _, b := range merges {
				c.log(c.ctx.Style.MigrateLabel("  branch " + b.mig.Name))
				for _, op := range b.ops {
					c.log("    " + m.FormattedDescription(op))
				}
			}
		}
		ok, err := q.AskMerge(app)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		biggest := 0
		found := false
		var leafNames []string
		var deps []m.Key
		for _, b := range merges {
			if n, ok := autodetector.ParseNumber(b.mig.Name); ok && (!found || n > biggest) {
				biggest, found = n, true
			}
			leafNames = append(leafNames, b.mig.Name)
			deps = append(deps, m.Key{App: app, Name: b.mig.Name})
		}
		if !found {
			biggest = 1
		}
		slices.Sort(leafNames)
		parts := []string{fmt.Sprintf("%04d", biggest+1)}
		if c.o.name != "" {
			parts = append(parts, c.o.name)
		} else {
			parts = append(parts, "merge")
			joined := strings.Join(leafNames, "_")
			if len(joined) > 47 {
				parts = append(parts, m.MigrationNameTimestamp())
			} else {
				parts = append(parts, joined)
			}
		}
		mig := &m.Migration{App: app, Name: strings.Join(parts, "_"), Dependencies: deps}
		w, path, err := migrationWriter(c.ctx.Project, mig, c.o.header)
		if err != nil {
			return err
		}
		if !c.dryRun {
			content, err := w.AsString()
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, content, 0o644); err != nil {
				return err
			}
			if c.ctx.Verbosity > 0 {
				c.log("created merge migration " + path)
				if c.o.scriptable {
					c.ctx.Stdout.Println(path)
				}
			}
		} else if c.ctx.Verbosity == 3 {
			content, err := w.AsString()
			if err != nil {
				return err
			}
			c.log(c.ctx.Style.MigrateHeading(fmt.Sprintf("full contents of %s:", filepath.Base(path))))
			c.log(string(content))
		}
	}
	return nil
}

// pythonListRepr renders a list of strings the way Python's repr does. The
// result reaches the user verbatim inside messages Django words identically.
func pythonListRepr(xs []string) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprintf("%v", x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func init() {
	writer.Now = func() time.Time { return time.Now().UTC() }
}
