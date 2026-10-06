---
title: Packs
tagline: The doorman's lines are a folder of audio. Swap the folder and a Victorian doorman, a bureaucrat or a ship's computer answers instead.
audience: Anyone who wants the stranger's greeting to sound like their house.
order: 100
---

## The problem

A phone system's personality is usually the one it shipped with. The bouncer
is the part of this project people remember, so it should be the part they can
change.

## What a pack is

Six lines, recorded as audio: the welcome a known caller hears, the lobby
greeting, the response to a wrong extension, "Good day", the message when a
room does not answer, and "connecting". A pack is a folder holding those six
in the two sample rates a phone call uses, plus a small manifest naming the
pack, its author, its licence and the text of each line.

Point the house at a different folder and the next stranger meets a different
doorman. Nothing else changes: the lobby's rules, the ladder and the ringing
are the same whichever voice delivers them.

## Making one

Write the six lines. `doorman pack build` renders them to audio through a
local open-source voice or a hosted one, in the voice you choose, and prints
the three commands that install the result. The bundled pack is built the same
way, so anything you can do to it you can do to your own.

## The rules

**Mechanisms are free, content is optional.** Everything the phone *does* is
open source and works completely with the bundled pack. Packs make it more
fun; nothing requires buying one. A feature that only worked with a purchased
pack would be a crippled program, and will not be built.

**Archetypes, never people.** A pack is a Victorian doorman, not a named
butler from television. No named characters, no living people, no impressions
of recognisable performers. The same rule applies to packs others publish.

**The format is public domain.** Build packs, publish them, sell them, no
permission needed. The bundled audio is Creative Commons, share-alike.

## Stories the child steers

*Planned, not shipped.* The same pack format carries a second kind: an
interactive audio story. A child dials an extension, the story plays, and the
keypad chooses what happens next. A story is one plain-text file, and the only
kind of link it can contain is a jump to another part of itself, so a small
person mashing the keypad ends up somewhere in the story rather than somewhere
in the phone system.

The format is published and public domain, the builder renders a story to
audio today, and the engine that tells one is written. The extension that puts
a call through to it is the piece that is next. When it ships, this page will
say so.
