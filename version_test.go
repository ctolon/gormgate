package gormgate

import (
	"runtime/debug"
	"testing"
)

// TestModuleVersion covers the cases the build information can present:
// gormgate linked as a released dependency, replaced by a checkout,
// replaced by another module version, and built from this repository.
func TestModuleVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  debug.Module
		want string
	}{
		{
			name: "released dependency",
			mod:  debug.Module{Path: modulePath, Version: "v0.1.0"},
			want: "0.1.0",
		},
		{
			name: "pseudo-version",
			mod:  debug.Module{Path: modulePath, Version: "v0.0.0-20260923120000-abcdef123456"},
			want: "0.0.0-20260923120000-abcdef123456",
		},
		{
			name: "replaced by a local checkout",
			mod: debug.Module{Path: modulePath, Version: "v0.0.0",
				Replace: &debug.Module{Path: "../..", Version: ""}},
			want: "(devel)",
		},
		{
			name: "replaced by another version",
			mod: debug.Module{Path: modulePath, Version: "v0.1.0",
				Replace: &debug.Module{Path: modulePath, Version: "v0.2.0"}},
			want: "0.2.0",
		},
		{
			name: "built from this repository",
			mod:  debug.Module{Path: modulePath, Version: "(devel)"},
			want: "(devel)",
		},
		{
			name: "no version recorded",
			mod:  debug.Module{Path: modulePath},
			want: "(devel)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := moduleVersion(&tc.mod); got != tc.want {
				t.Errorf("moduleVersion() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestVersionIsReported checks that Version finds this module in its own
// build information rather than falling through to the default. The tests
// run inside the repository, so the version it finds is "(devel)".
func TestVersionIsReported(t *testing.T) {
	if got := Version(); got != "(devel)" {
		t.Errorf("Version() = %q, want %q when built from the repository", got, "(devel)")
	}
}
