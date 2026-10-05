package host

import (
	"context"
	"os/exec"
)

// System runs commands for real.
type System struct{}

func (System) Run(ctx context.Context, argv ...string) (string, error) {
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	return string(out), err
}
