package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// The mail hook: doorman prints, something else sends.
//
// MAIL_HOOK names an executable and MAIL_TO the house mailbox. The hook
// gets the subject as its one argument, a Markdown body on stdin, MAIL_TO
// in its environment, and answers with its exit status. The shipped hook
// (scripts/mail-hook-bullmoose) runs the bullmoose CLI; any mail system is
// a ten-line script with the same contract. doorman itself never learns
// SMTP, JMAP or a mail provider's name, and nothing on the call path ever
// invokes this: the two callers are `doorman inbox` (a reply the house
// sent) and `doorman digest` (yesterday, every morning), both off the phone.

// mailHookTimeout bounds one send. A hook that hangs on a dead network
// must not hold the inbox loop or the digest timer open.
const mailHookTimeout = 60 * time.Second

// mailer sends one message, or reports why it could not.
type mailer func(subject, body string) error

// newMailer resolves MAIL_HOOK and MAIL_TO through env. Nil when neither is
// set, which is every install that has no house mailbox; an error when one
// is set without the other, because half a configuration is a message
// somebody expects and never gets.
func newMailer(env func(string) (string, bool)) (mailer, error) {
	hook, _ := env("MAIL_HOOK")
	to, _ := env("MAIL_TO")
	hook, to = strings.TrimSpace(hook), strings.TrimSpace(to)
	switch {
	case hook == "" && to == "":
		return nil, nil
	case hook == "":
		return nil, fmt.Errorf("MAIL_TO is set but MAIL_HOOK is not — the executable that sends (scripts/mail-hook-bullmoose ships)")
	case to == "":
		return nil, fmt.Errorf("MAIL_HOOK is set but MAIL_TO is not — the house mailbox address")
	}
	return func(subject, body string) error {
		ctx, cancel := context.WithTimeout(context.Background(), mailHookTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, hook, subject)
		cmd.Stdin = strings.NewReader(body)
		cmd.Env = append(os.Environ(), "MAIL_TO="+to)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		cmd.Stdout = &stderr
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if len(msg) > 300 {
				msg = msg[:300] + "…"
			}
			if msg == "" {
				return fmt.Errorf("mail hook: %w", err)
			}
			return fmt.Errorf("mail hook: %w: %s", err, msg)
		}
		return nil
	}, nil
}
