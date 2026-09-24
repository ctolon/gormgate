// Package loader builds the migration graph from the compiled migrations and
// the applied-migration records.
//
// django: db/migrations/loader.py
package loader

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ctolon/gormgate/internal/graph"
	"github.com/ctolon/gormgate/internal/writer"
	m "github.com/ctolon/gormgate/migrations"
)

// AppSpec describes one installed app.
type AppSpec struct {
	// Label is the app label.
	Label string
	// Dir is the migrations directory on disk ("" when unknown, e.g. in a
	// deployed binary without sources).
	Dir string
	// Disabled skips the app: it is treated as having no migrations.
	Disabled bool
}

// Applied lists applied migrations in recording order.
type Applied interface {
	AppliedMigrations() ([]m.Key, error)
}

// Config configures a loader.
type Config struct {
	// Apps are the installed apps whose migrations to load.
	Apps []AppSpec
	// Registered returns the compiled migrations of an app; defaults to
	// migrations.Registered.
	Registered func(app string) []*m.Migration
	// Compiled reports whether the app's migrations package is compiled in;
	// defaults to migrations.RegisteredPackageDir.
	Compiled func(app string) bool
	// Recorder supplies the applied migrations; nil when there is no
	// connection, in which case nothing counts as applied.
	Recorder Applied
	// IgnoreNoMigrations keeps a dependency on an app without migrations
	// from being an error.
	IgnoreNoMigrations bool
	// ReplaceMigrations lets a squashed migration stand in for the ones
	// it replaces. Callers set it explicitly; the zero value keeps the
	// replaced migrations in the graph.
	ReplaceMigrations bool
	// SkipDiskCheck disables the comparison of compiled migrations with the
	// files on disk.
	SkipDiskCheck bool
}

// CommandError reports a project that is not set up as the loader needs:
// migrations that are not compiled in, or that do not match the files on
// disk. The commands print it and exit rather than showing a stack.
type CommandError struct{ Msg string }

func (e *CommandError) Error() string { return e.Msg }

// MigrationNotFoundError reports that no migration of an app matches the
// name prefix that was asked for.
//
// django: loader.py MigrationLoader.get_migration_by_prefix (KeyError)
type MigrationNotFoundError struct{ Msg string }

func (e *MigrationNotFoundError) Error() string { return e.Msg }

// Loader holds a project's migration graph, built from the migrations
// compiled into the command and the records of which ones are applied.
//
// django: loader.py MigrationLoader
type Loader struct {
	cfg   Config
	Graph *graph.Graph
	// DiskMigrations are the migrations found for the installed apps.
	DiskMigrations map[m.Key]*m.Migration
	// Applied is the set of applied migrations; AppliedOrder lists them in
	// the order they were recorded, which is the order the commands
	// report them in.
	Applied      map[m.Key]bool
	AppliedOrder []m.Key
	// UnmigratedApps and MigratedApps partition the installed apps.
	UnmigratedApps map[string]bool
	MigratedApps   map[string]bool
	// Replacements are the squashed migrations, by their own key.
	Replacements map[m.Key]*m.Migration
	// progress tracks replaceMigration's recursion: a key absent has not
	// been visited, mapped to false is in progress, true is done.
	progress map[m.Key]bool
}

// New builds a loader and its graph.
func New(cfg Config) (*Loader, error) {
	if cfg.Registered == nil {
		cfg.Registered = m.Registered
	}
	if cfg.Compiled == nil {
		cfg.Compiled = func(app string) bool {
			if _, ok := m.RegisteredPackageDir(app); ok {
				return true
			}
			return len(m.Registered(app)) > 0
		}
	}
	l := &Loader{cfg: cfg}
	if err := l.BuildGraph(); err != nil {
		return nil, err
	}
	return l, nil
}

var migrationFile = regexp.MustCompile(`^\d{4}_\w*\.go$`)

// loadDisk loads the compiled migrations of all apps.
//
// django: loader.py MigrationLoader.load_disk
func (l *Loader) loadDisk() error {
	l.DiskMigrations = map[m.Key]*m.Migration{}
	l.UnmigratedApps = map[string]bool{}
	l.MigratedApps = map[string]bool{}
	for _, app := range l.cfg.Apps {
		if app.Disabled {
			l.UnmigratedApps[app.Label] = true
			continue
		}
		compiled := l.cfg.Compiled(app.Label)
		files, dirExists, err := migrationFiles(app.Dir)
		if err != nil {
			return err
		}
		if !compiled {
			if len(files) > 0 && !l.cfg.SkipDiskCheck {
				return &CommandError{Msg: fmt.Sprintf("the migrations of app '%s' in %s are not compiled into this command. Import the package from the gorm-gate command (makemigrations generates zz_gormgate_migrations.go for this)", app.Label, app.Dir)}
			}
			l.UnmigratedApps[app.Label] = true
			continue
		}
		l.MigratedApps[app.Label] = true
		registered := l.cfg.Registered(app.Label)
		for _, mig := range registered {
			if mig.App != app.Label {
				return &m.BadMigrationError{Msg: fmt.Sprintf("migration %s registered under app '%s' declares app '%s'", mig.Name, app.Label, mig.App)}
			}
			l.DiskMigrations[mig.Key()] = mig.Clone()
		}
		if dirExists && !l.cfg.SkipDiskCheck {
			if err := checkDisk(app, files, registered); err != nil {
				return err
			}
		}
	}
	return nil
}

func migrationFiles(dir string) (map[string]string, bool, error) {
	if dir == "" {
		return nil, false, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	files := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !migrationFile.MatchString(e.Name()) {
			continue
		}
		files[writer.MigrationNameFromFile(e.Name())] = filepath.Join(dir, e.Name())
	}
	return files, true, nil
}

// checkDisk compares the compiled migrations with the files on disk, so a
// stale binary can't silently apply the wrong history.
func checkDisk(app AppSpec, files map[string]string, registered []*m.Migration) error {
	reg := map[string]*m.Migration{}
	for _, mig := range registered {
		reg[mig.Name] = mig
	}
	var missing, extra []string
	for name := range files {
		if _, ok := reg[name]; !ok {
			missing = append(missing, files[name])
		}
	}
	for name, mig := range reg {
		if _, ok := files[name]; !ok {
			extra = append(extra, name)
			continue
		}
		if mig.File != "" && filepath.Base(mig.File) != filepath.Base(files[name]) {
			return &m.BadMigrationError{Msg: fmt.Sprintf("migration %s.%s is registered from %s, expected file %s", app.Label, name, mig.File, files[name])}
		}
	}
	slices.Sort(missing)
	slices.Sort(extra)
	if len(missing) > 0 {
		return &CommandError{Msg: fmt.Sprintf("migration file(s) %s exist on disk but are not compiled into this command; rebuild it", strings.Join(missing, ", "))}
	}
	if len(extra) > 0 {
		return &CommandError{Msg: fmt.Sprintf("migration(s) %s of app '%s' are compiled into this command but their files are missing from %s; rebuild it", strings.Join(extra, ", "), app.Label, app.Dir)}
	}
	return nil
}

// GetMigration returns the named migration node.
func (l *Loader) GetMigration(app, name string) (*m.Migration, bool) {
	mig, ok := l.Graph.Nodes[m.Key{App: app, Name: name}]
	return mig, ok && mig != nil
}

// GetMigrationByPrefix returns the migration of app whose name starts with
// prefix.
//
// django: loader.py MigrationLoader.get_migration_by_prefix
func (l *Loader) GetMigrationByPrefix(app, prefix string) (*m.Migration, error) {
	var results []m.Key
	for k := range l.DiskMigrations {
		if k.App == app && strings.HasPrefix(k.Name, prefix) {
			results = append(results, k)
		}
	}
	if len(results) > 1 {
		return nil, &m.AmbiguityError{Msg: fmt.Sprintf("there is more than one migration for '%s' with the prefix '%s'", app, prefix)}
	}
	if len(results) == 0 {
		return nil, &MigrationNotFoundError{Msg: fmt.Sprintf("there is no migration for '%s' with the prefix '%s'", app, prefix)}
	}
	return l.DiskMigrations[results[0]], nil
}

// django: loader.py MigrationLoader.check_key
func (l *Loader) checkKey(key m.Key, currentApp string) (*m.Key, error) {
	if (key.Name != "__first__" && key.Name != "__latest__") || l.Graph.Has(key) {
		return &key, nil
	}
	if key.App == currentApp || l.UnmigratedApps[key.App] {
		return nil, nil
	}
	if l.MigratedApps[key.App] {
		var nodes []m.Key
		if key.Name == "__first__" {
			nodes = l.Graph.RootNodes(key.App)
		} else {
			nodes = l.Graph.LeafNodes(key.App)
		}
		if len(nodes) == 0 {
			if l.cfg.IgnoreNoMigrations {
				return nil, nil
			}
			return nil, &m.ValueError{Msg: "dependency on app with no migrations: " + key.App}
		}
		return &nodes[0], nil
	}
	return nil, &m.ValueError{Msg: "dependency on unknown app: " + key.App}
}

func (l *Loader) sortedDiskKeys() []m.Key {
	return slices.SortedFunc(maps.Keys(l.DiskMigrations), graph.CompareKeys)
}

// django: loader.py MigrationLoader.add_internal_dependencies
func (l *Loader) addInternalDependencies(key m.Key, mig *m.Migration) error {
	for _, parent := range mig.Dependencies {
		if parent.App == key.App && parent.Name != "__first__" {
			if err := l.Graph.AddDependency(mig.String(), key, parent, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// django: loader.py MigrationLoader.add_external_dependencies
func (l *Loader) addExternalDependencies(key m.Key, mig *m.Migration) error {
	for _, parent := range mig.Dependencies {
		if key.App == parent.App {
			continue
		}
		p, err := l.checkKey(parent, key.App)
		if err != nil {
			return err
		}
		if p != nil {
			if err := l.Graph.AddDependency(mig.String(), key, *p, true); err != nil {
				return err
			}
		}
	}
	for _, child := range mig.RunBefore {
		c, err := l.checkKey(child, key.App)
		if err != nil {
			return err
		}
		if c != nil {
			if err := l.Graph.AddDependency(mig.String(), *c, key, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// django: loader.py MigrationLoader._resolve_replaced_migration_keys
func (l *Loader) resolveReplacedKeys(mig *m.Migration) map[m.Key]bool {
	out := map[m.Key]bool{}
	for _, k := range mig.Replaces {
		if entry, ok := l.DiskMigrations[k]; ok && len(entry.Replaces) > 0 {
			for rk := range l.resolveReplacedKeys(entry) {
				out[rk] = true
			}
		} else {
			out[k] = true
		}
	}
	return out
}

// replaceMigration folds one squashed migration into the graph, replacing
// the migrations it lists once their applied state agrees. It recurses into
// squashed migrations that themselves replace squashed migrations.
//
// django: loader.py MigrationLoader.replace_migration
func (l *Loader) replaceMigration(key m.Key) error {
	if done, seen := l.progress[key]; seen {
		if done {
			return nil
		}
		return &CommandError{Msg: fmt.Sprintf("cyclical squash replacement found, starting at %s", graph.FormatKey(key))}
	}
	l.progress[key] = false
	mig := l.Replacements[key]
	for _, rk := range mig.Replaces {
		if _, ok := l.Replacements[rk]; ok {
			if err := l.replaceMigration(rk); err != nil {
				return err
			}
		}
	}
	replaced := l.resolveReplacedKeys(mig)
	allApplied, someApplied := true, false
	for k := range replaced {
		if l.Applied[k] {
			someApplied = true
		} else {
			allApplied = false
		}
	}
	if allApplied {
		l.markApplied(key)
	} else {
		l.unmarkApplied(key)
	}
	// The squashed migration stands in for the ones it replaces only when
	// their applied state is unambiguous: either all or none are applied.
	var err error
	if allApplied || !someApplied {
		err = l.Graph.RemoveReplacedNodes(key, mig.Replaces)
	} else {
		err = l.Graph.RemoveReplacementNode(key, mig.Replaces)
	}
	if err != nil {
		return err
	}
	l.progress[key] = true
	return nil
}

func (l *Loader) markApplied(k m.Key) {
	if !l.Applied[k] {
		l.Applied[k] = true
		l.AppliedOrder = append(l.AppliedOrder, k)
	}
}

func (l *Loader) unmarkApplied(k m.Key) {
	if !l.Applied[k] {
		return
	}
	delete(l.Applied, k)
	if i := slices.Index(l.AppliedOrder, k); i >= 0 {
		l.AppliedOrder = slices.Delete(l.AppliedOrder, i, i+1)
	}
}

// BuildGraph (re)builds the graph from the compiled migrations and the
// database.
//
// django: loader.py MigrationLoader.build_graph
func (l *Loader) BuildGraph() error {
	if err := l.loadDisk(); err != nil {
		return err
	}
	l.Applied = map[m.Key]bool{}
	l.AppliedOrder = nil
	if l.cfg.Recorder != nil {
		keys, err := l.cfg.Recorder.AppliedMigrations()
		if err != nil {
			return err
		}
		for _, k := range keys {
			l.markApplied(k)
		}
	}
	l.Graph = graph.New()
	l.Replacements = map[m.Key]*m.Migration{}
	keys := l.sortedDiskKeys()
	for _, k := range keys {
		mig := l.DiskMigrations[k]
		l.Graph.AddNode(k, mig)
		if len(mig.Replaces) > 0 {
			l.Replacements[k] = mig
		}
	}
	for _, k := range keys {
		if err := l.addInternalDependencies(k, l.DiskMigrations[k]); err != nil {
			return err
		}
	}
	for _, k := range keys {
		if err := l.addExternalDependencies(k, l.DiskMigrations[k]); err != nil {
			return err
		}
	}
	if l.cfg.ReplaceMigrations {
		l.progress = map[m.Key]bool{}
		for _, k := range keys {
			if _, ok := l.Replacements[k]; ok {
				if err := l.replaceMigration(k); err != nil {
					return err
				}
			}
		}
	}
	if err := l.Graph.ValidateConsistency(); err != nil {
		var nf *m.NodeNotFoundError
		if errors.As(err, &nf) {
			var candidates []m.Key
			for _, rk := range sortedReplacementKeys(l.Replacements) {
				for _, r := range l.Replacements[rk].Replaces {
					if r == nf.Node {
						candidates = append(candidates, rk)
					}
				}
			}
			if len(candidates) > 0 {
				replaced := false
				for _, c := range candidates {
					if l.Graph.Has(c) {
						replaced = true
					}
				}
				if !replaced {
					var tries []string
					for _, c := range candidates {
						tries = append(tries, c.App+"."+c.Name)
					}
					return &m.NodeNotFoundError{
						Message: fmt.Sprintf("Migration %s depends on nonexistent node ('%s', '%s'). gormgate tried to replace migration %s.%s with any of [%s] but wasn't able to because some of the replaced migrations are already applied.",
							nf.Origin, nf.Node.App, nf.Node.Name, nf.Node.App, nf.Node.Name, strings.Join(tries, ", ")),
						Node: nf.Node,
					}
				}
			}
		}
		return err
	}
	return l.Graph.EnsureNotCyclic()
}

func sortedReplacementKeys(r map[m.Key]*m.Migration) []m.Key {
	return slices.SortedFunc(maps.Keys(r), graph.CompareKeys)
}

// CheckConsistentHistory returns a *migrations.InconsistentMigrationHistory
// when an applied migration has dependencies that are not applied.
//
// django: loader.py MigrationLoader.check_consistent_history
func (l *Loader) CheckConsistentHistory(rec Applied, alias string) error {
	keys, err := rec.AppliedMigrations()
	if err != nil {
		return err
	}
	applied := map[m.Key]bool{}
	for _, k := range keys {
		applied[k] = true
	}
	for _, k := range keys {
		if !l.Graph.Has(k) {
			continue
		}
		parents := make([]m.Key, 0)
		for p := range l.Graph.NodeMap[k].Parents {
			parents = append(parents, p.Key)
		}
		graph.SortKeys(parents)
		for _, p := range parents {
			if applied[p] || l.AllReplacedApplied(p, applied) {
				continue
			}
			return &m.InconsistentMigrationHistory{Msg: fmt.Sprintf("migration %s.%s is applied before its dependency %s.%s on database '%s'", k.App, k.Name, p.App, p.Name, alias)}
		}
	}
	return nil
}

// AllReplacedApplied reports whether every migration the squashed migration
// k replaces counts as applied, following chains of squashes. It is false
// when k is not a squashed migration.
//
// django: loader.py MigrationLoader.all_replaced_applied
func (l *Loader) AllReplacedApplied(k m.Key, applied map[m.Key]bool) bool {
	r, ok := l.Replacements[k]
	if !ok {
		return false
	}
	for _, rk := range r.Replaces {
		if !applied[rk] && !l.AllReplacedApplied(rk, applied) {
			return false
		}
	}
	return true
}

// DetectConflicts returns apps with more than one leaf migration.
//
// django: loader.py MigrationLoader.detect_conflicts
func (l *Loader) DetectConflicts() map[string][]string {
	seen := map[string][]string{}
	conflicting := map[string]bool{}
	for _, leaf := range l.Graph.LeafNodes("") {
		if _, ok := seen[leaf.App]; ok {
			conflicting[leaf.App] = true
		}
		seen[leaf.App] = append(seen[leaf.App], leaf.Name)
	}
	out := map[string][]string{}
	for app := range conflicting {
		names := slices.Sorted(slices.Values(seen[app]))
		out[app] = names
	}
	return out
}

// ProjectState returns the model state as of nodes, after them when atEnd
// is set and before them otherwise. Nil nodes means the graph's leaves.
// realModels are the models of the unmigrated apps.
//
// django: loader.py MigrationLoader.project_state
func (l *Loader) ProjectState(nodes []m.Key, atEnd bool, realModels []*m.ModelState) (*m.ProjectState, error) {
	return l.Graph.MakeState(nodes, atEnd, l.UnmigratedApps, realModels)
}
