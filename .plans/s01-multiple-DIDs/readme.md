Multi-DID, categorised
================
§7a — routing spine. Hard prerequisite. Dialplan names the line, one policy file per line, router multiplexes. Separate files rather than [[lines]] sections specifically so a typo in the business policy can't take down the home line — invariant 4 generalised. No app arg means today's behaviour byte for byte, and internal/lobby doesn't change at all, because Deps.Policy is already a function.

§7b — line identity. The [line] section, per-line prompts, and the disposition knob. Curt doorman and courteous concierge, one engine, opposite defaults.

§7c — outbound identity. Marked do not ship 7a without this. The dialplan sets no caller ID at all today.

§7d — per-line observability. A line field on the call record — one log, not one per line, so a day still reads in order.

§7b, 7c and 7d are independent of each other once 7a exists.