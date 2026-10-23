package main

import (
	"errors"
	"log/slog"
	"sort"
	"strings"

	"callmemaybe/internal/configcheck"
	"callmemaybe/internal/events"
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
// after it. A line whose current files are refused at startup runs its last
// good copy (policy.last-good/<line>, written each time a load is accepted)
// and says so loudly. Only without a usable copy is a named line dropped —
// calls arriving on it are served by the default line, the same place an
// unknown name lands — and a default line returned as an error, because that
// is the whole phone rather than one number.
//
// Every line is validated by configcheck.Options, the rules `doorman check`
// and sync apply, with [line] trunk checked against trunksPath at every load.
// Validation problems are logged through policy.RedactProblem: a message may
// quote a number or a PIN from the file, and neither belongs in a log.
func openLines(files []policy.LineFile, handsetsPath, trunksPath string, watch bool, log *slog.Logger, journal *events.Writer) ([]openLine, error) {

	var opened []openLine
	for _, lf := range files {
		// The default line's logger is left exactly as it was, so an install
		// with one line logs byte for byte what it logged before lines
		// existed. A named line is a new thing and says which it is.
		lineLog := log
		if lf.Name != policy.DefaultLine {
			lineLog = log.With("line", lf.Name)
		}

		// Unknown keys are refused here as in `doorman check` and sync — one
		// validator (call-me-maybe#131). Each is still named on its own line,
		// so the reason a reload was refused is easy to find. Only names are
		// logged; a key's value never is.
		opts := configcheck.Options(nil)
		opts.TrunksPath = trunksPath
		opts.OnUnknownKey = func(u policy.UnknownKey) {
			lineLog.Warn("unrecognised key in config",
				"file", u.File, "key", u.Path, "didYouMean", u.Suggest,
				"hint", "run `doorman check` for the full report")
		}
		opts.LastGood = policy.LastGoodPath(lf.Path, lf.Name)
		opts.Bootstrap = true
		opts.OnLastGoodError = func(err error) {
			lineLog.Warn("cannot save the last good copy of the policy; the policy is in service, but a restart after a bad edit has nothing to fall back to",
				"lastGood", opts.LastGood, "problem", policy.RedactProblem(err.Error(), 0))
		}

		store, err := policy.OpenStoreWith(lf.Path, handsetsPath, watch, opts,
			func(p *policy.Policy) {
				if journal != nil {
					e := events.System(events.ConfigReloaded, "policy", 0)
					e.Line = lf.Name
					journal.Post(e)
				}
				lineLog.Info("policy reloaded",
					"allowList", p.AllowListCount(), "extensions", p.ExtensionCount())
			},
			func(err error) {
				if journal != nil {
					e := events.System(events.ConfigReloadFailed, "keeping-previous-policy", 0)
					e.Line = lf.Name
					journal.Post(e)
				}
				lineLog.Error("policy reload failed, keeping previous version",
					"problem", policy.RedactProblem(err.Error(), 0))
			},
		)
		if err != nil {
			if lf.Name == policy.DefaultLine {
				// The only error this can be is the default line's, and the
				// caller already knows its path.
				return opened, errors.New(policy.RedactProblem(err.Error(), 0))
			}
			// Fail loudly where an operator will see it, and keep answering.
			lineLog.Error("line policy will not load — calls on this line are served by the default line instead",
				"path", lf.Path, "problem", policy.RedactProblem(err.Error(), 0))
			continue
		}
		opened = append(opened, openLine{lf.Name, store, lineLog})
		if refused := store.Fallback(); refused != nil {
			// One line that cannot be missed: the box is answering calls, but
			// not with the files on disk, or not under the current rules.
			reason, msg := "running-last-good-policy", "POLICY REFUSED AT STARTUP — running the last good configuration until the files are fixed"
			if store.PreviousRules() {
				reason, msg = "running-policy-under-previous-rules", "POLICY REFUSED BY THE CURRENT RULES — running it under the previous release's rules until the files are fixed"
			}
			if journal != nil {
				e := events.System(events.ConfigReloadFailed, reason, 0)
				e.Line = lf.Name
				journal.Post(e)
			}
			lineLog.Error(msg,
				"path", lf.Path, "lastGood", opts.LastGood, "problem", policy.RedactProblem(refused.Error(), 0),
				"hint", "run `doorman check` for the full report")
		}

		pol := store.Current()
		// onNoInput is on the startup line because it is the one policy value
		// whose default is invisible in the file and audible on the phone: a
		// line that was meant to take messages and silently dismisses instead
		// looks exactly like a line nobody has called yet. The prompt pack is
		// here for the same reason — a missing pack degrades to silence.
		lineLog.Info("policy loaded", "path", lf.Path,
			"allowList", pol.AllowListCount(), "extensions", pol.ExtensionCount(),
			"pinLength", pol.PinLength, "onNoInput", pol.Line().OnNoInput,
			"prompts", orEnvPrompts(pol.Line().Prompts))
	}
	return opened, nil
}

// orEnvPrompts names the pack a line actually speaks from. An empty [line]
// prompts is not "no pack" — it is PROMPT_MEDIA_PREFIX — and a log line saying
// "" would read as the former.
func orEnvPrompts(prefix string) string {
	if prefix == "" {
		return "(PROMPT_MEDIA_PREFIX)"
	}
	return prefix
}

// recordedLine is the line name as the call log carries it: empty for the
// default line.
//
// The same rule as the default line's logger, which openLines leaves unadorned
// for the same reason — an install with one number writes exactly the records
// it wrote before lines existed, and nothing about it has to read the word
// "line". calls.Record.LineOrDefault resolves the empty case on the way back
// out, so nothing downstream has to know about this.
func recordedLine(name string) string {
	if name == policy.DefaultLine {
		return ""
	}
	return name
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
	// order is the sequence lines were added in, which is DiscoverLines'
	// order: the default line first, then the rest sorted. The outbound
	// console offers them in exactly this order, so the digit that reaches a
	// line is stable across restarts and cannot move when a policy file is
	// added beside the others.
	order []string
}

func newLineSet() *lineSet {
	return &lineSet{byName: make(map[string]lobby.Deps)}
}

// add registers a line. The default line is held separately as well, because
// it is the answer to every question this type cannot otherwise answer.
func (l *lineSet) add(name string, deps lobby.Deps) {
	if _, dup := l.byName[name]; !dup {
		l.order = append(l.order, name)
	}
	l.byName[name] = deps
	if name == policy.DefaultLine {
		l.deflt = deps
	}
}

// outbound assembles the outbound plan from every line's *current* policy.
//
// Read per console call rather than resolved once at startup, for the same
// reason Deps.Policy is a func: editing outbound_cid in a policy file is an
// ordinary policy edit and goes live on the next reload. The plain dialplan
// path is the exception and cannot be — that caller ID is baked into the
// generated PJSIP config, so it changes when `doorman render` runs. The
// runbook says so where an operator will read it.
func (l *lineSet) outbound() outboundPlan {
	ids := make([]lineIdentity, 0, len(l.order))
	for _, name := range l.order {
		ids = append(ids, lineIdentity{Name: name, LineIdentity: l.byName[name].Policy().Line()})
	}
	return newOutboundPlan(ids)
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
