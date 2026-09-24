package management

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ctolon/gormgate/internal/fromgorm"
)

// Level is the severity of a check message.
type Level int

// The severity levels, from least to most severe. A message of LevelError
// or above makes the command fail.
const (
	LevelDebug    Level = 10
	LevelInfo     Level = 20
	LevelWarning  Level = 30
	LevelError    Level = 40
	LevelCritical Level = 50
)

// CheckMessage is one system check result.
type CheckMessage struct {
	// Level is how serious the message is.
	Level Level
	// Msg says what is wrong and Hint how to fix it.
	Msg  string
	Hint string
	// Obj names what the message is about ("?" when unknown), and ID is
	// the check's identifier, which Project.SilencedChecks can silence.
	Obj string
	ID  string
}

// String renders a message like Django's CheckMessage.__str__.
func (c CheckMessage) String() string {
	obj := c.Obj
	if obj == "" {
		obj = "?"
	}
	id := ""
	if c.ID != "" {
		id = "(" + c.ID + ") "
	}
	hint := ""
	if c.Hint != "" {
		hint = "\n\tHINT: " + c.Hint
	}
	return obj + ": " + id + c.Msg + hint
}

// IsSerious reports level >= ERROR.
func (c CheckMessage) IsSerious() bool { return c.Level >= LevelError }

// systemChecks runs the model checks: the current models must convert to a
// valid, renderable state.
func systemChecks(p *Project) []CheckMessage {
	st, err := p.CurrentState()
	if err != nil {
		var ce *fromgorm.CheckError
		if errors.As(err, &ce) {
			return []CheckMessage{{Level: LevelError, Msg: ce.Msg, Obj: ce.Obj, ID: ce.ID}}
		}
		return []CheckMessage{{Level: LevelError, Msg: err.Error(), ID: "gormgate.E300"}}
	}
	if _, err := st.Apps(); err != nil {
		// Rendering reports every unresolved reference at once, joined
		// with errors.Join, so each one becomes its own check message.
		problems := []error{err}
		if j, ok := err.(interface{ Unwrap() []error }); ok {
			problems = j.Unwrap()
		}
		out := make([]CheckMessage, len(problems))
		for i, p := range problems {
			out[i] = CheckMessage{Level: LevelError, Msg: p.Error(), ID: "fields.E307"}
		}
		return out
	}
	return nil
}

// The tags a system check carries.  Django labels every check so that
// --tag can select it and --list-tags can list the labels; gormgate's
// checks are not Django's, so its tags name what it actually checks.
const (
	// TagModels labels the checks over the model definitions: the models
	// of the project must convert to a valid state whose references all
	// resolve.
	TagModels = "models"
	// TagDatabase labels the checks that need a connection, which only
	// run against the aliases --database names -- Django's rule for its
	// own database checks.
	TagDatabase = "database"
	// TagCompatibility labels the checks that compare what the models
	// declare with what the project and its databases can actually do.
	// Django has a tag of this name, so the command line stays Django's,
	// but the checks under it are gormgate's own -- Django has no
	// counterpart to port, because it owns its own model layer and its own
	// connections.  checks_compat.go holds them.
	TagCompatibility = "compatibility"
)

// checkOptions is what the caller of the checks framework asks for.
type checkOptions struct {
	// AppLabels limits the model checks to these apps; none means every
	// installed app.
	AppLabels []string
	// Tags limits the run to the checks carrying one of these tags; none
	// means every check.
	Tags []string
	// Databases are the connections the database checks run against.
	// None means they do not run at all, which is why every other command
	// still only checks models.
	Databases []string
	// Deploy includes the deployment checks. gormgate registers none --
	// Django's are about Django's own settings -- so it changes nothing,
	// and the option exists so that a Django habit does not fail.
	Deploy bool
	// DisplayNumErrors appends the "System check identified N issues (M
	// silenced)." footer and, when there is nothing else to say, makes
	// that footer the whole output.  Only the check command asks for it.
	DisplayNumErrors bool
	// FailLevel is the level at or above which a message makes the
	// command fail.  Zero means LevelError, which is Django's default.
	FailLevel Level
}

// registeredCheck is one check function together with its tags.
type registeredCheck struct {
	tags []string
	run  func(*Context, checkOptions) []CheckMessage
}

// registeredChecks is gormgate's check registry.
var registeredChecks = []registeredCheck{
	{tags: []string{TagModels}, run: modelChecks},
	{tags: []string{TagDatabase}, run: databaseChecks},
	{tags: []string{TagCompatibility}, run: compatibilityChecks},
}

// tagsAvailable returns the sorted tags of the registered checks.
func tagsAvailable() []string {
	var tags []string
	for _, c := range registeredChecks {
		for _, t := range c.tags {
			if !slices.Contains(tags, t) {
				tags = append(tags, t)
			}
		}
	}
	slices.Sort(tags)
	return tags
}

// tagExists reports whether any registered check carries tag.
func tagExists(tag string) bool {
	return slices.Contains(tagsAvailable(), tag)
}

// modelChecks runs the model checks, restricted to the apps the caller
// named.
func modelChecks(c *Context, o checkOptions) []CheckMessage {
	return forApps(c.Project, systemChecks(c.Project), o.AppLabels)
}

// forApps keeps the messages that belong to the named apps; no label keeps
// all of them.  A message is attributed to an app by the label its Obj
// names, which is how the model checks spell what they are about
// ("blog.Post").  A message that names no installed app -- a problem of the
// project as a whole, such as two apps whose models claim the same table --
// is kept whichever apps were named, because there is no app to attribute
// it to.
func forApps(p *Project, msgs []CheckMessage, labels []string) []CheckMessage {
	if len(labels) == 0 {
		return msgs
	}
	var out []CheckMessage
	for _, msg := range msgs {
		app, _, named := strings.Cut(msg.Obj, ".")
		if named && p.App(app) != nil && !slices.Contains(labels, app) {
			continue
		}
		out = append(out, msg)
	}
	return out
}

// databaseChecks opens each of the named connections, which is what runs a
// backend's own guards: the server version must be supported, and openGauss
// must be in PostgreSQL compatibility mode.  A connection that cannot be
// opened is a check message rather than a failed command, so that "check
// --database default" reports an unreachable server the way it reports a
// broken model.
func databaseChecks(c *Context, o checkOptions) []CheckMessage {
	var out []CheckMessage
	for _, alias := range o.Databases {
		if _, err := c.Project.Conn(alias); err != nil {
			out = append(out, CheckMessage{Level: LevelError, Msg: err.Error(), Obj: alias, ID: "gormgate.E001"})
		}
	}
	return out
}

// runChecks is the checks framework: it runs the selected checks, prints
// what they found the way Django does, and fails on a message at or above
// the fail level.
func runChecks(c *Context, o checkOptions) error {
	failLevel := o.FailLevel
	if failLevel == 0 {
		failLevel = LevelError
	}
	var all []CheckMessage
	for _, rc := range registeredChecks {
		if len(o.Tags) > 0 && !slices.ContainsFunc(rc.tags, func(t string) bool { return slices.Contains(o.Tags, t) }) {
			continue
		}
		all = append(all, rc.run(c, o)...)
	}
	silenced := map[string]bool{}
	for _, s := range c.Project.SilencedChecks {
		silenced[s] = true
	}
	visible := 0
	groups := []struct {
		name     string
		min, max Level
	}{{"CRITICALS", LevelCritical, 1 << 30}, {"ERRORS", LevelError, LevelCritical}, {"WARNINGS", LevelWarning, LevelError}, {"INFOS", LevelInfo, LevelWarning}, {"DEBUGS", 0, LevelInfo}}
	var body strings.Builder
	for _, g := range groups {
		var lines []string
		for _, msg := range all {
			if silenced[msg.ID] || msg.Level < g.min || msg.Level >= g.max {
				continue
			}
			s := msg.String()
			if msg.IsSerious() {
				s = c.Style.Error(s)
			} else {
				s = c.Style.Warning(s)
			}
			lines = append(lines, s)
		}
		if len(lines) > 0 {
			visible += len(lines)
			slices.Sort(lines)
			fmt.Fprintf(&body, "\n%s:\n%s\n", g.name, strings.Join(lines, "\n"))
		}
	}
	// What fails the command is the fail level, not the severity the
	// message was styled with: --fail-level WARNING makes a warning fatal.
	serious := slices.ContainsFunc(all, func(msg CheckMessage) bool {
		return !silenced[msg.ID] && msg.Level >= failLevel
	})
	header, footer := "", ""
	if visible > 0 {
		header = "System check identified some issues:\n"
	}
	if o.DisplayNumErrors {
		if visible > 0 {
			footer = "\n"
		}
		footer += fmt.Sprintf("System check identified %s (%d silenced).", issueCount(visible), len(all)-visible)
	}
	if serious {
		return &SystemCheckError{msg: c.Style.Error(header) + body.String() + footer}
	}
	msg := header + body.String() + footer
	if msg == "" {
		return nil
	}
	// Django writes to stderr only when it has issues to show; the bare
	// "no issues" footer goes to stdout.
	if visible > 0 {
		c.Stderr.PrintlnStyled(msg, plain)
	} else {
		c.Stdout.Println(msg)
	}
	return nil
}

// issueCount renders the count in the footer the way Django words it.
func issueCount(n int) string {
	switch n {
	case 0:
		return "no issues"
	case 1:
		return "1 issue"
	default:
		return fmt.Sprintf("%d issues", n)
	}
}

// RunChecks runs the system checks a command runs before it does its work,
// and fails on a serious message.  The check command calls runChecks
// itself, because it is the only caller that selects checks, chooses the
// fail level and asks for the issue-count footer.
func RunChecks(c *Context) error { return runChecks(c, checkOptions{}) }
