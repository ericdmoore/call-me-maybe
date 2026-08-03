package story

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Runtime limits. Almost everything about a story is checked statically, but
// these two cannot be: cycles are legitimate in the genre — "you wander back
// to the entrance" is the whole shape of it — so a loop cannot be rejected at
// build time and gets a budget instead.
const (
	// MaxTransitions is far beyond what a person pressing digits can reach
	// and is hit immediately by a runaway cycle.
	MaxTransitions = 5000
	// MaxDuration is generous on purpose. Being cut off mid-story is a worse
	// failure than a long call, and a child will happily listen for an hour.
	MaxDuration = 60 * time.Minute
)

// A Teller is what the story talks through. An interface rather than a
// concrete session for the same reason lobby.ARI is one: the interpreter has
// to be drivable by a test with no Asterisk anywhere.
type Teller interface {
	// Play plays one clip to completion, or until a digit interrupts it.
	// A returned digit is a barge-in and counts as the caller's answer.
	Play(ctx context.Context, clip string) (barge string, err error)
	// Gather waits for one digit. An empty return means the timeout expired.
	Gather(ctx context.Context, timeout time.Duration) (digit string, err error)
}

// ErrLimit ends a story that ran too long or went round too many times.
var ErrLimit = errors.New("story: limit reached")

// Result describes how a telling ended, for the call log and for tests.
type Result struct {
	// Node is where the story stopped.
	Node string
	// Transitions taken.
	Transitions int
	// Elapsed wall clock.
	Elapsed time.Duration
	// Ended is why: "end", "hangup", "limit".
	Ended string
}

// Play tells the story. It returns when the story ends, the caller hangs up
// (which arrives as a cancelled context), or a limit is reached.
//
// The caller hanging up needs no handling here beyond selecting on the
// context: cancellation is the state, and every wait in Teller honours it.
func (s *Story) Play(ctx context.Context, t Teller, now func() time.Time) (Result, error) {
	if now == nil {
		now = time.Now
	}
	started := now()
	res := Result{Node: Slug(s.Meta.Start)}

	node, ok := s.Nodes[res.Node]
	if !ok {
		return res, fmt.Errorf("story: start node %q does not exist", s.Meta.Start)
	}

	for {
		if err := ctx.Err(); err != nil {
			res.Ended, res.Elapsed = "hangup", now().Sub(started)
			return res, nil
		}
		if res.Transitions > MaxTransitions {
			res.Ended, res.Elapsed = "limit", now().Sub(started)
			return res, fmt.Errorf("%w: %d transitions", ErrLimit, res.Transitions)
		}
		if el := now().Sub(started); el > MaxDuration {
			res.Ended, res.Elapsed = "limit", el
			return res, fmt.Errorf("%w: %v", ErrLimit, el)
		}

		barge, err := s.speak(ctx, t, node)
		if err != nil {
			return res, err
		}
		if ctx.Err() != nil {
			res.Ended, res.Elapsed = "hangup", now().Sub(started)
			return res, nil
		}

		// Nothing to decide: the node either continues or the story is over.
		if node.End || (len(node.Choices) == 0 && node.Goto == "") {
			res.Ended, res.Elapsed = "end", now().Sub(started)
			return res, nil
		}
		if node.Goto != "" {
			next, ok := s.Nodes[node.Goto]
			if !ok {
				return res, fmt.Errorf("story: node %q goes to %q, which does not exist", node.ID, node.Goto)
			}
			node, res.Node, res.Transitions = next, next.ID, res.Transitions+1
			continue
		}

		// A digit during playback is the answer; otherwise wait for one.
		digit := barge
		target, outcome := "", ""
		for tries := 0; ; tries++ {
			if digit == "" {
				digit, err = t.Gather(ctx, node.Behaviour.Timeout)
				if err != nil {
					return res, err
				}
			}
			if ctx.Err() != nil {
				res.Ended, res.Elapsed = "hangup", now().Sub(started)
				return res, nil
			}

			// Repeat is free everywhere and needs no state, which is why a
			// child can always get the scene again.
			if digit == RepeatDigit {
				outcome = "repeat"
				break
			}
			if c, ok := choiceFor(node, digit); ok {
				target = c.Target
				break
			}

			// No input and unrecognised input are different situations and
			// get different answers: the first caller is thinking, the second
			// is engaged but wrong and does not want the scene replayed.
			rule := node.Behaviour.OnInvalid
			if digit == "" {
				rule = node.Behaviour.OnTimeout
			}
			if tries >= node.Behaviour.Retries {
				outcome = rule
				break
			}
			if rule == "reprompt" {
				if _, err := s.prompt(ctx, t, node); err != nil {
					return res, err
				}
			} else if rule == "repeat" {
				if b, err := s.speak(ctx, t, node); err != nil {
					return res, err
				} else if b != "" {
					digit = b
					continue
				}
			} else {
				outcome = rule // an anchor or "end" applies immediately
				break
			}
			digit = ""
		}

		switch {
		case target != "":
			// chosen below
		case outcome == "repeat" || outcome == "reprompt":
			res.Transitions++
			continue
		case outcome == "end":
			res.Ended, res.Elapsed = "end", now().Sub(started)
			return res, nil
		case strings.HasPrefix(outcome, "#"):
			target = Slug(strings.TrimPrefix(outcome, "#"))
		default:
			res.Ended, res.Elapsed = "end", now().Sub(started)
			return res, nil
		}

		next, ok := s.Nodes[target]
		if !ok {
			return res, fmt.Errorf("story: node %q points at %q, which does not exist", node.ID, target)
		}
		node, res.Node, res.Transitions = next, next.ID, res.Transitions+1
	}
}

// speak plays a node's lines, stopping early on a barge-in digit.
func (s *Story) speak(ctx context.Context, t Teller, n *Node) (barge string, err error) {
	for _, l := range n.Lines {
		if ctx.Err() != nil {
			return "", nil
		}
		clip := l.Clip
		switch l.Verb {
		case "":
		case "sfx":
			clip = "sfx/" + l.Attrs["name"]
		case "pause":
			continue // handled by the Teller's own pacing; nothing to play
		default:
			continue
		}
		d, err := t.Play(ctx, clip)
		if err != nil {
			return "", err
		}
		if d != "" {
			return d, nil
		}
	}
	return "", nil
}

// prompt replays only the choice lines, which is what an impatient caller who
// pressed the wrong key actually wants.
func (s *Story) prompt(ctx context.Context, t Teller, n *Node) (barge string, err error) {
	for _, c := range n.Choices {
		if ctx.Err() != nil {
			return "", nil
		}
		d, err := t.Play(ctx, "choice/"+n.ID+"-"+c.Digit)
		if err != nil {
			return "", err
		}
		if d != "" {
			return d, nil
		}
	}
	return "", nil
}

func choiceFor(n *Node, digit string) (Choice, bool) {
	for _, c := range n.Choices {
		if c.Digit == digit {
			return c, true
		}
	}
	return Choice{}, false
}
