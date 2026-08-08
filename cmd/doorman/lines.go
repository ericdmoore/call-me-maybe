package main

import (
	"log/slog"
	"sort"
	"strings"

	"callmemaybe/internal/lobby"
	"callmemaybe/internal/policy"
)

// openLine is one line, its policy store, and the logger everything about it
// is reported through.
type openLine struct {
	name  string
	store *policy.Store
	log   *slog.Logger
}

// openLines opens one policy.Store per line.
//
// One store per file is the reason lines are separate files rather than
// sections of one. Invariant 4 says an invalid policy must never take the
// phone down; per-line stores generalise it to *an invalid policy must never
// take down a line it does not belong to*, at startup and on every reload
// after it. A named line that will not load is reported and dropped — calls
// arriving on it are served by the default line, the same place an unknown
// name lands. A default line that will not load is returned as an error,
// because that is the whole phone rather than one number.
func openLines(files []policy.LineFile, handsetsPath string, watch bool, log *slog.Logger) ([]openLine, error) {
	var opened []openLine
	for _, lf := range files {
		// The default line's logger is left exactly as it was, so an install
		// with one line logs byte for byte what it logged before lines
		// existed. A named line is a new thing and says which it is.
		lineLog := log
		if lf.Name != policy.DefaultLine {
			lineLog = log.With("line", lf.Name)
		}

		// Unknown keys warn here and refuse in `doorman check`. The daemon
		// must keep answering the phone (invariant 4), so a key it does not
		// recognise is something to say loudly and then ignore — which is what
		// the decoder was already doing, minus the saying. Only names are
		// logged; a key's value never is.
		opts := policy.Options{
			OnUnknownKey: func(u policy.UnknownKey) {
				lineLog.Warn("unrecognised key in config, ignoring it",
					"file", u.File, "key", u.Path, "didYouMean", u.Suggest,
					"hint", "run `doorman check` for the full report")
			},
		}

		store, err := policy.OpenStoreWith(lf.Path, handsetsPath, watch, opts,
			func(p *policy.Policy) {
				lineLog.Info("policy reloaded",
					"allowList", p.AllowListCount(), "extensions", p.ExtensionCount())
			},
			func(err error) {
				lineLog.Error("policy reload failed, keeping previous version", "err", err)
			},
		)
		if err != nil {
			if lf.Name == policy.DefaultLine {
				// Returned unwrapped: the only error this can be is the
				// default line's, and the caller already knows its path.
				return opened, err
			}
			// Fail loudly where an operator will see it, and keep answering.
			lineLog.Error("line policy will not load — calls on this line are served by the default line instead",
				"path", lf.Path, "err", err)
			continue
		}
		opened = append(opened, openLine{lf.Name, store, lineLog})

		pol := store.Current()
		lineLog.Info("policy loaded", "path", lf.Path,
			"allowList", pol.AllowListCount(), "extensions", pol.ExtensionCount(),
			"pinLength", pol.PinLength)
	}
	return opened, nil
}

// lineSet is the whole of multi-line routing: a map from the name the dialplan
// passes as a Stasis argument to the Deps that serve it.
//
// This lives in the router and nowhere else. lobby.Deps.Policy is already
// func() *policy.Policy, so a line is just a different Deps — the state
// machine never learns that lines exist, and `git diff internal/lobby` staying
// empty is the check on that.
//
// Each line also gets its own *lobby.RateLimiter rather than sharing one with
// the line folded into the key: the key is built inside internal/lobby, and
// reaching in there to prefix it is exactly the change this design exists to
// avoid. Separate instances give the same guarantee — the lines have opposite
// dispositions by design, so a redialler burning through a permissive business
// line's budget must not lock out the house.
type lineSet struct {
	deflt  lobby.Deps
	byName map[string]lobby.Deps
}

func newLineSet() *lineSet {
	return &lineSet{byName: make(map[string]lobby.Deps)}
}

// add registers a line. The default line is held separately as well, because
// it is the answer to every question this type cannot otherwise answer.
func (l *lineSet) add(name string, deps lobby.Deps) {
	l.byName[name] = deps
	if name == policy.DefaultLine {
		l.deflt = deps
	}
}

func (l *lineSet) names() []string {
	out := make([]string, 0, len(l.byName))
	for name := range l.byName {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// forCall picks the line a StasisStart belongs to.
//
// Every path through here returns a usable Deps. doorman cannot read the
// dialplan, so it cannot validate the set of line names at startup, which
// leaves exactly one safe behaviour for a name it does not have: serve the
// caller on the default line and say so loudly. Dropping the call would turn
// a typo in a config file into a number that silently stops answering.
func (l *lineSet) forCall(args []string, log *slog.Logger) lobby.Deps {
	if len(args) == 0 || args[0] != "line" {
		// No line argument is the compatibility guarantee: an install whose
		// dialplan predates this feature reaches exactly the Deps it always
		// did. So does an argument this router does not recognise.
		return l.deflt
	}
	if len(args) < 2 || args[1] == "" {
		log.Warn("dialplan passed a line argument with no line name, using the default line",
			"hint", "the Stasis call takes two arguments: Stasis(${DOORMAN_APP},line,<name>)")
		return l.deflt
	}
	if deps, ok := l.byName[args[1]]; ok {
		return deps
	}
	log.Warn("dialplan named a line that has no policy file, using the default line",
		"line", args[1], "known", strings.Join(l.names(), ", "),
		"hint", "create policy."+args[1]+".toml beside policy.toml, or fix the Stasis argument")
	return l.deflt
}
