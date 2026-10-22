package main

import (
	"context"

	"callmemaybe/internal/activation"
)

func observeActivation(lines []openLine) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go activation.Serve(ctx, activation.DefaultDir, func() map[string]string {
		current := make(map[string]string, len(lines))
		for _, line := range lines {
			current[line.name] = line.store.Current().SourceDigest()
		}
		return current
	})
	return cancel
}
