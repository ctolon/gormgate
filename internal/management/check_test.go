package management

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ctolon/gormgate/internal/termcolors"
)

// checkContext builds the context a command run gets, with captured
// streams and no styling.
func checkContext(p *Project) (*Context, *bytes.Buffer, *bytes.Buffer) {
	env, out, errOut := testEnv("")
	return &Context{
		Project: p, Env: env,
		Stdout: NewStream(env.Stdout, env.StdoutIsTTY),
		Stderr: NewStream(env.Stderr, env.StderrIsTTY),
		Style:  termcolors.NoStyle(), Prog: "manage.py",
	}, out, errOut
}

// stubChecks replaces the check registry for the duration of a test, which
// is how the framework is exercised with messages gormgate's own checks
// cannot produce (a warning, a silenced message).
func stubChecks(t *testing.T, checks ...registeredCheck) {
	t.Helper()
	saved := registeredChecks
	registeredChecks = checks
	t.Cleanup(func() { registeredChecks = saved })
}

// fixedCheck is a check that always reports the same messages.
func fixedCheck(tag string, msgs ...CheckMessage) registeredCheck {
	return registeredCheck{tags: []string{tag}, run: func(*Context, checkOptions) []CheckMessage { return msgs }}
}

// TestCheckListTags lists the tags of the registered checks, on stdout.
func TestCheckListTags(t *testing.T) {
	for _, argv := range [][]string{{"--list-tags"}, {"--list-tags", "--deploy"}} {
		env, out, errOut := testEnv("")
		code := runIn(env, testProject(), append([]string{"check"}, argv...)...)
		if code != 0 {
			t.Fatalf("%v exited %d: %s", argv, code, errOut.String())
		}
		// gormgate registers no deployment checks, so --deploy adds no
		// tags.
		if got, want := out.String(), "compatibility\ndatabase\nmodels\n"; got != want {
			t.Errorf("check %v printed %q, want %q", argv, got, want)
		}
		if errOut.Len() != 0 {
			t.Errorf("check %v wrote %q to stderr", argv, errOut.String())
		}
	}
}

// TestCheckUnknownTag reports a tag no check carries, before running any.
func TestCheckUnknownTag(t *testing.T) {
	env, _, errOut := testEnv("")
	code := runIn(env, testProject(), "check", "--tag", "nosuchtag")
	if code != 1 {
		t.Fatalf("exit status %d, want 1", code)
	}
	if got, want := errOut.String(), "gorm-gate: no check carries the \"nosuchtag\" tag\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

// TestCheckUnknownApp reports an app label the project does not have.
func TestCheckUnknownApp(t *testing.T) {
	env, _, errOut := testEnv("")
	code := runIn(env, testProject(), "check", "nosuchapp")
	if code != 1 {
		t.Fatalf("exit status %d, want 1", code)
	}
	if got, want := errOut.String(), "gorm-gate: no app named \"nosuchapp\"; is it in the project's Apps?\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

// TestCheckTagFiltering runs only the checks carrying a selected tag.
func TestCheckTagFiltering(t *testing.T) {
	stubChecks(t,
		fixedCheck(TagModels, CheckMessage{Level: LevelWarning, Msg: "a model is odd", Obj: "blog.Post", ID: "blog.W001"}),
		fixedCheck(TagDatabase, CheckMessage{Level: LevelWarning, Msg: "a database is odd", Obj: "default", ID: "db.W001"}),
	)
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{argv: nil, want: "2 issues"},
		{argv: []string{"--tag", "models"}, want: "1 issue"},
		{argv: []string{"-t", "database"}, want: "1 issue"},
		{argv: []string{"-t", "models", "-t", "database"}, want: "2 issues"},
	} {
		env, _, errOut := testEnv("")
		argv := append([]string{"check"}, tc.argv...)
		if code := runIn(env, testProject(), argv...); code != 0 {
			t.Fatalf("%v exited %d: %s", tc.argv, code, errOut.String())
		}
		want := "System check identified " + tc.want + " (0 silenced)."
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("check %v:\n%s\ndoes not report %q", tc.argv, errOut.String(), want)
		}
	}
}

// TestCheckFailLevel pins that --fail-level decides the exit status: a
// warning passes at the default ERROR and fails at WARNING.
func TestCheckFailLevel(t *testing.T) {
	stubChecks(t, fixedCheck(TagModels, CheckMessage{Level: LevelWarning, Msg: "a model is odd", Obj: "blog.Post", ID: "blog.W001"}))
	for _, tc := range []struct {
		level    string
		wantCode int
	}{
		{level: "", wantCode: 0},
		{level: "CRITICAL", wantCode: 0},
		{level: "ERROR", wantCode: 0},
		{level: "WARNING", wantCode: 1},
		{level: "INFO", wantCode: 1},
		{level: "DEBUG", wantCode: 1},
	} {
		env, _, errOut := testEnv("")
		argv := []string{"check"}
		if tc.level != "" {
			argv = append(argv, "--fail-level", tc.level)
		}
		code := runIn(env, testProject(), argv...)
		if code != tc.wantCode {
			t.Errorf("--fail-level %q exited %d, want %d:\n%s", tc.level, code, tc.wantCode, errOut.String())
		}
		if tc.wantCode == 1 && !strings.HasPrefix(errOut.String(), "System check identified some issues:") {
			t.Errorf("--fail-level %q did not report a check failure:\n%s", tc.level, errOut.String())
		}
	}
}

// TestCheckFailLevelChoices rejects a level that is not one of Django's.
func TestCheckFailLevelChoices(t *testing.T) {
	env, _, _ := testEnv("")
	if code := runIn(env, testProject(), "check", "--fail-level", "FATAL"); code != 2 {
		t.Errorf("an unknown --fail-level exited %d, want 2", code)
	}
}

// TestCheckSilenced counts the silenced messages in the footer instead of
// reporting them, and does not let a silenced error fail the command.
func TestCheckSilenced(t *testing.T) {
	stubChecks(t, fixedCheck(TagModels,
		CheckMessage{Level: LevelError, Msg: "silenced", Obj: "blog.Post", ID: "blog.E001"},
		CheckMessage{Level: LevelWarning, Msg: "shown", Obj: "blog.Post", ID: "blog.W002"},
	))
	p := testProject()
	p.SilencedChecks = []string{"blog.E001"}
	env, out, errOut := testEnv("")
	if code := runIn(env, p, "check"); code != 0 {
		t.Fatalf("a silenced error failed the command (exit %d):\n%s", code, errOut.String())
	}
	want := "System check identified some issues:\n\nWARNINGS:\nblog.Post: (blog.W002) shown\n\nSystem check identified 1 issue (1 silenced).\n"
	if got := errOut.String(); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	if out.Len() != 0 {
		t.Errorf("issues were written to stdout: %q", out.String())
	}
}

// TestCheckNoIssues pins the branch the check command is the only caller
// of: with nothing to report the footer is the whole output and it goes to
// stdout, not stderr.
func TestCheckNoIssues(t *testing.T) {
	stubChecks(t)
	env, out, errOut := testEnv("")
	if code := runIn(env, testProject(), "check"); code != 0 {
		t.Fatalf("exit status %d: %s", code, errOut.String())
	}
	if got, want := out.String(), "System check identified no issues (0 silenced).\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", errOut.String())
	}
}

// TestRunChecksIsSilentWhenClean pins that the other commands, which call
// RunChecks, still print nothing when the checks pass: the issue-count
// footer belongs to the check command alone.
func TestRunChecksIsSilentWhenClean(t *testing.T) {
	stubChecks(t)
	ctx, out, errOut := checkContext(testProject())
	if err := RunChecks(ctx); err != nil {
		t.Fatalf("RunChecks: %v", err)
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("RunChecks wrote stdout %q, stderr %q", out.String(), errOut.String())
	}
}

// TestForApps limits the model checks to the named apps. A message that
// names no installed app is a problem of the project as a whole and is kept
// whichever apps were named.
func TestForApps(t *testing.T) {
	p := &Project{Apps: []*App{{Label: "blog"}, {Label: "auth"}}, Command: "manage.py"}
	msgs := []CheckMessage{
		{Level: LevelWarning, Msg: "blog", Obj: "blog.Post", ID: "blog.W001"},
		{Level: LevelWarning, Msg: "auth", Obj: "auth.User", ID: "auth.W001"},
		{Level: LevelWarning, Msg: "project", ID: "gormgate.W001"},
	}
	ids := func(got []CheckMessage) []string {
		out := make([]string, len(got))
		for i, m := range got {
			out[i] = m.ID
		}
		return out
	}
	if got := ids(forApps(p, msgs, nil)); strings.Join(got, ",") != "blog.W001,auth.W001,gormgate.W001" {
		t.Errorf("no app label dropped something: %v", got)
	}
	if got := ids(forApps(p, msgs, []string{"blog"})); strings.Join(got, ",") != "blog.W001,gormgate.W001" {
		t.Errorf("forApps(blog) = %v", got)
	}
	if got := ids(forApps(p, msgs, []string{"auth"})); strings.Join(got, ",") != "auth.W001,gormgate.W001" {
		t.Errorf("forApps(auth) = %v", got)
	}
}

// TestCheckDatabaseChecksAreOptIn pins Django's rule: the checks that need
// a connection only run against the aliases --database names, so no other
// command and no bare "check" opens one.
func TestCheckDatabaseChecksAreOptIn(t *testing.T) {
	opened := 0
	stubChecks(t, registeredCheck{tags: []string{TagDatabase}, run: func(_ *Context, o checkOptions) []CheckMessage {
		opened += len(o.Databases)
		return nil
	}})
	env, _, errOut := testEnv("")
	if code := runIn(env, testProject(), "check"); code != 0 {
		t.Fatalf("exit status %d: %s", code, errOut.String())
	}
	if opened != 0 {
		t.Errorf("a bare check asked for %d connections", opened)
	}
}

// TestDatabaseChecksReportAFailedConnection turns a connection that cannot
// be opened into a check message rather than a failed command.
func TestDatabaseChecksReportAFailedConnection(t *testing.T) {
	ctx, _, _ := checkContext(testProject())
	msgs := databaseChecks(ctx, checkOptions{Databases: []string{"nosuch"}})
	if len(msgs) != 1 || msgs[0].Level != LevelError || msgs[0].ID != "gormgate.E001" || msgs[0].Obj != "nosuch" {
		t.Fatalf("databaseChecks = %+v", msgs)
	}
	if !strings.Contains(msgs[0].Msg, "doesn't exist") {
		t.Errorf("message = %q", msgs[0].Msg)
	}
}

// TestIssueCount pins the wording of the footer's count, which Django
// spells out for none and one.
func TestIssueCount(t *testing.T) {
	for n, want := range map[int]string{0: "no issues", 1: "1 issue", 2: "2 issues"} {
		if got := issueCount(n); got != want {
			t.Errorf("issueCount(%d) = %q, want %q", n, got, want)
		}
	}
}
