Brand marks
===========

**Direction chosen: The Threshold.** A stepped Deco portal with light spilling
out — the lobby with nobody in it, so the doorman is implied by the door he
isn't standing in. A figure in a peaked cap was the first attempt and was
rejected as too literal.

## The ornateness ladder

Same idea at three settings. Pick by context rather than picking one forever.

| File | Setting | Where it works |
|---|---|---|
| `mark-threshold.svg` | plain | Small sizes, inline in text, anywhere the frame would clutter |
| `mark-threshold-framed.svg` | framed | Chamfered cartouche, bead run, fluted flanks. The middle setting. |
| `mark-threshold-ornate.svg` | ornate | Nested frames, bead border, corner ray-fans, lozenge. Hero and wordmark lockups. |

**At 22px the ornate version collapses into a bright dot** — the frame stops
helping and starts hurting. Use `ornate` large, and something plainer for the
favicon. That is a feature of the ladder, not a defect in one rung.

## Ornament

| File | What it is |
|---|---|
| `ornament-dial-divider.svg` | Section divider. The Dial mark demoted from logo to rosette, with paired rules, beads, and stepped terminals. Give it width; height sits at 48. |
| `pattern-deco.svg` | Seamless 80×80 tile. `background-repeat: repeat; background-size: 80px`. |

The pattern tiles because every element meeting an edge is either centred on
that edge or mirrored on the opposite one — four tiles meeting at a corner
complete the quarter-fan into a full rosette. Change the scale rather than the
file; at 160px the individual motifs read, at 80px it reads as texture.

## Also in here, not chosen

| File | Verdict |
|---|---|
| `mark-dial.svg` | Clever, not the logo. Lives on as the divider above. |
| `mark-two-doors.svg` | Carries the most meaning; needs room to read. |
| `mark-winged-band.svg` | Mercury as messenger — right idea, execution did not land. |
| `mark-chime.svg` | Too obvious. |

## Conventions

**Single colour via `currentColor`.** No light and dark variants to keep in
sync — set `color` on a parent and the mark follows. Opacity variation inside
each file does the tonal work.

**Primitives, not traced paths.** Arcs, trapezoids, rects, circles. Crisp at any
size, editable by hand, and no bitmaps anywhere — matching the site.

Drawn from the shared vocabulary in the references — bilateral symmetry,
radiating rays, stepped setbacks, bead borders, nested frames, lozenges,
fluting — without reproducing any specific work.
