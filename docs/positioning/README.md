# Communication review — October 5, 2026

Call Me Maybe can lead with **a child calling a friend independently**, then
explain why a family might want to own and maintain its phone system. The
current live homepage leads with its incoming-call mechanism and reaches the
family motivation only indirectly. The doorman metaphor is memorable; it
works better as the explanation after the reason to care.

Reviewed repository commit: `29adb13` (starting checkout). Live HTML fetched
from https://callmemaybe.cc/ and https://callmemaybe.cc/providers/ on October 5,
2026. The homepage's wording matches the checked-out source. Findings below
refer to that baseline, before this change. Code inspection is evidence of
implementation, not proof of a successful live phone deployment.

## Deliverables

- [Feature and claim audit, with roadmap nudges](communication-audit.md)
- [Three neutral positioning article drafts](comparison-drafts.md)
- [Four short fictional family narratives](story-drafts.md)
- [Provider research and attribution conventions](provider-research.md)

The articles and stories are editorial drafts in this directory, not published
routes or customer testimonials. Site changes in this working tree are
reviewable source changes; no deployment was performed.

## Suggested positioning

**A home phone for calling friends, without handing over a smartphone.**

Open-source software you run at home, with your choice of phones and provider.
Route calls around the household, give handsets a bedtime, and connect the
phone to the home automation you already use.

This is a strong fit for a household with someone willing to maintain Linux,
Asterisk and a carrier account. A managed family-phone service can be the
better choice when easy setup and a single support provider matter more.

Do not turn “screen-free” into a universal hardware claim: some supported SIP
phones have displays, and softphones run on smartphones. Do not promise
approved-contact-only outgoing calls; that is not what the stock dialplan does.

## Verification

- Astro production build passed: 16 generated pages.
- All generated internal link targets exist; all 22 rendered provider links
  have source, medium, campaign and content UTM parameters.
- UTM helper preserves unrelated query parameters and fragments, replaces
  previous tags without duplication, and is idempotent.
- Three light-use cost calculations checked independently.
- Chromium checked the homepage, provider page and three changed feature
  pages at 390px and 1440px widths: HTTP 200, one heading, no page-wide
  horizontal overflow or JavaScript errors. Provider tables scroll within
  their own focusable regions on narrow screens. Desktop rendering inspected.
- `git diff --check` passed.
- `make check` and binary schema generation could not run: this workspace
  lacks `gofmt`, and `/usr/bin/go` is not the Go compiler (`Unknown option:
  vet` / `build`). No Go code or configuration schema changed. No live
  carrier calls or emergency calls were placed.
