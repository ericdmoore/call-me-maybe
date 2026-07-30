Brand marks
===========

Five Art Deco logo directions. Pick one (or one plus a small-size fallback) and
it gets wired into the site header, the favicon, and the README.

| File | Mark | The idea |
|---|---|---|
| `mark-dial.svg` | The Dial | A rotary dial and a Deco rosette are the same drawing. Says *telephone* before you read a word. |
| `mark-threshold.svg` | The Threshold | The lobby with nobody in it — light spilling from a stepped portal. The doorman is implied by the door. |
| `mark-two-doors.svg` | Two Doors | The product's thesis: one open to light, one barred. Symmetric frame, asymmetric contents. |
| `mark-winged-band.svg` | The Winged Band | Mercury's helmet as a band and blades. Mercury is the messenger; so is a telephone. |
| `mark-chime.svg` | The Chime | A house as a Deco tower, ringing. Six shapes, so it survives the smallest sizes. |

## Conventions

Every mark is **single-colour** via `currentColor`, so it inherits from whatever
it sits in — no light and dark variants to keep in sync. Set `color` on a parent
and the mark follows.

All geometry is built from primitives: arcs, trapezoids, rects, circles. No
traced path data and no bitmaps, matching the site, which means these stay crisp
at any size and stay editable by hand.

Drawn from the shared vocabulary in the reference images — strict bilateral
symmetry, radiating rays, stepped ziggurat setbacks, stacked facets, paired
rules — without reproducing any specific work.

## Deliberately not a doorman

A figure in a peaked cap was the obvious first move and it was rejected as too
literal. Each of these reaches the idea sideways instead: the object he uses,
the door he guards, the decision he makes, the god he descends from, or the
sound he is answering.

## Checking one before committing to it

The 20px column on the contact sheet is the favicon test, and it is where marks
usually die. `mark-chime` and `mark-dial` hold up smallest; `mark-two-doors`
carries the most meaning but needs room to read.
