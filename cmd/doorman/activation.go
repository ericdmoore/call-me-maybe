package main

import (
	"context"

	"callmemaybe/internal/activation"
	"callmemaybe/internal/webhookconfig"
)

func observeActivation(lines []openLine, passes *passList, hooks ...*webhookconfig.Store) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go activation.Serve(ctx, activation.DefaultDir, func() map[string]string {
		return activationDigests(lines, passes, hooks...)
	})
	return cancel
}

// activationDigests is what the daemon is running: each line's compiled
// sources, and the lobby pass list's file hash ("" for none) when the daemon
// reads one.
func activationDigests(lines []openLine, passes *passList, hooks ...*webhookconfig.Store) map[string]string {
	current := make(map[string]string, len(lines)+1)
	for _, line := range lines {
		current[line.name] = line.store.Current().SourceDigest()
	}
	if passes != nil {
		current[passActivationKey] = passes.Hash()
	}
	for _, hook := range hooks {
		current[webhookconfig.ActivationKey] = hook.Current().Hash
	}
	return current
}
