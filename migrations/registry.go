package migrations

import (
	"cmp"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
)

// The registry collects the migrations that generated migration files
// register from their init functions. It is where the loader finds an app's
// compiled migrations, in place of Django's import of the app's migrations
// package.
var (
	registryMu sync.Mutex
	registry   = map[Key]*Migration{}
	packages   = map[string]string{} // app label -> directory of the package
)

// Register adds a migration. It is called from generated migration files.
// It panics on a migration it cannot use: one without App or Name, one
// registered twice, or one whose operations carry an argument of a shape the
// compiler cannot check (see validateOperations).
func Register(m *Migration) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if m.App == "" || m.Name == "" {
		panic("gormgate: migrations.Register requires App and Name")
	}
	if err := validateOperations(m.Operations); err != nil {
		panic(fmt.Sprintf("gormgate: migration %s: %s", m.Key(), err))
	}
	if _, file, _, ok := runtime.Caller(1); ok && m.File == "" {
		m.File = file
	}
	if prev, dup := registry[m.Key()]; dup {
		panic(fmt.Sprintf("gormgate: migration %s registered twice (%s and %s)", m.Key(), prev.File, m.File))
	}
	registry[m.Key()] = m
}

// RegisterPackage records the directory of an app's migrations package. It
// is called from the package's migrations.go so that the loader can compare
// the compiled registry with the files on disk.
func RegisterPackage(app string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, file, _, ok := runtime.Caller(1); ok {
		packages[app] = filepath.Dir(file)
	}
}

// Registered returns the registered migrations of app sorted by name.
func Registered(app string) []*Migration {
	registryMu.Lock()
	defer registryMu.Unlock()
	var out []*Migration
	for k, m := range registry {
		if k.App == app {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b *Migration) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// RegisteredPackageDir returns the compiled source directory recorded by
// RegisterPackage.
func RegisteredPackageDir(app string) (string, bool) {
	registryMu.Lock()
	defer registryMu.Unlock()
	d, ok := packages[app]
	return d, ok
}
