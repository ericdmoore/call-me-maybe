package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"callmemaybe/internal/lobby"
	"callmemaybe/internal/policy"
)

// What `doorman check` says about a public line: the arithmetic challenge and
// the clips it speaks from.

// soundsDir is Asterisk's sounds tree. A var so a test can point it at a
// temporary directory. check only ever stats beneath it — it never reads
// audio — and only when it exists, which is to say only on the box: a
// workstation or CI validating examples has no sounds tree and is told
// nothing about clips it could not possibly have.
var soundsDir = "/var/lib/asterisk/sounds"

// describeChallenge is the one line check prints for a challenge a line asks.
func describeChallenge(p *policy.Policy) string {
	r := p.Challenge()
	return fmt.Sprintf("\"what is A times B\", %s; MAX_PIN_ATTEMPTS retries, each a new question.\n"+
		"                         A right answer forgives no PIN failure; wrong ones count against\n"+
		"                         the line's challenge budget (RATELIMIT_*), never its PIN budget.",
		r.Describe())
}

// missingClips reports which of the named clips have no file under the media
// prefix in the sounds tree, in the order given. Asterisk picks the format,
// so any extension counts. ok is false when there is no sounds tree to look
// in, and the caller then says nothing at all.
func missingClips(prefix string, clips []string) (missing []string, ok bool) {
	if st, err := os.Stat(soundsDir); err != nil || !st.IsDir() {
		return nil, false
	}
	for _, clip := range clips {
		matches, _ := filepath.Glob(filepath.Join(soundsDir, prefix, clip) + ".*")
		if len(matches) == 0 {
			missing = append(missing, clip)
		}
	}
	return missing, true
}

// warnChallengeClips prints a warning when the bundled challenge set is not
// installed. A warning, never a failure: the call still works — a missing
// clip is silence, not a stuck call — but a caller asked a question they
// cannot hear presses nothing and is dismissed.
func warnChallengeClips() {
	missing, ok := missingClips(lobby.ChallengeMediaPrefix(), lobby.ChallengeClips())
	if !ok || len(missing) == 0 {
		return
	}
	fmt.Printf("  ! the challenge clips are not all installed: %s has no %s.\n"+
		"    Callers will hear silence where the question should be. Build them with\n"+
		"    prompts/challenge.sh and install them as RUNBOOK \"The arithmetic challenge\" says.\n",
		filepath.Join(soundsDir, lobby.ChallengeMediaPrefix()), strings.Join(missing, ", "))
}
