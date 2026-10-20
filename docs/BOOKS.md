# Books on a handset

Dial **`*2665` (`*BOOK`)** from an authenticated local handset. This is the
Books slice of [#116](https://github.com/ericdmoore/call-me-maybe/issues/116).
Games (`*4263`) and Interactive Stories (`*24673`, `*CHOSE`) remain follow-up
work; this change does not claim those dial codes or replace their engines.

The menu reads nine titles per page in catalog order. Choose with `1`–`9`,
including while the announcements are playing. `#` advances to the next page;
on the last page it asks for a page number followed by `#`. `*` repeats and
`0` backs up one level. Zero in the page picker returns to its originating
page; zero at the Books root ends this directly dialed call. Three invalid or
unanswered attempts back out. A browse session freezes its title order and
page count, but checks a selected recording is still installed before playing.
New imports appear on the next call. Missing recordings never shift a digit
to a different title during a call.

Choose a book, then `1` to resume (announced only with a saved position), `2`
to start at the beginning, or `3` for chapters. Chapters use the same menu.
Preview imports announce “Preview recording” so excerpts are not represented
as complete books. The end of a preview returns to its book menu.

| During playback | Action |
| --- | --- |
| `1` / `3` | Previous / next chapter |
| `2` / `8` | Faster / slower by 0.1x, between 0.5x and 2.0x |
| `4` / `6` | Back / forward ten seconds within this chapter |
| `5` | Pause / play |
| `*` | Hear these controls; another key can interrupt the help |
| `0` | Save position and return to this book's menu |

Listening continues across internal buffers and chapter boundaries without
requiring another key. Speed and position survive visits to menus. Bookmarks
are private to the authenticated handset and the book/language/audio revision;
they do not follow caller ID or a person to another phone. Simultaneous calls
from one handset use the last saved position. A paused call backs out after
one minute of silence. An abruptly killed process may lose at most one
30-second output window (up to 60 seconds of source audio at 2x); ordinary
key presses and Asterisk hangup responses save the returned sample offset.

## Prepare recordings already on the box

Install the optional Books tools once on Debian/Ubuntu:

```sh
sudo apt-get install --no-install-recommends ffmpeg espeak-ng
```

Create a source catalog referencing **existing local audio**. This is an
import description, not a separately maintained phone menu. Regenerate it
from the content producer's recording manifest when adding chapters. Order
here determines spoken order; no cloud/store purchase is inferred from files.
No audiobook text is synthesized by this command, and no paid API is called.
Titles and common menu prompts are generated offline with espeak-ng.

```json
{
  "schema": 1,
  "books": [
    {
      "id": "sample-book",
      "title": "Sample Book",
      "language": "en",
      "origin": "cli",
      "preview": true,
      "chapters": [
        { "title": "Chapter one", "audio": "recordings/chapter-01.mp3" },
        { "title": "Chapter two", "audio": "recordings/chapter-02.mp3" }
      ]
    }
  ]
}
```

Paths are relative to the source JSON file or absolute. Inputs must be local
regular files; network inputs are refused. `origin` is `cli`, `store` or
`browser`, supplied by the importing operator. This field records provenance,
not a purchase entitlement. Language identifies the recording and bookmark;
this initial menu voice speaks English. At most 128 books, 999 chapters per
book and 24 hours per chapter are accepted.
Both the source JSON and the prepared catalog must fit within 2 MiB.
Oversized imports are rejected without replacing the previous catalog.

```sh
sudo doorman books prepare --source /path/to/catalog.json
sudo install -d -o asterisk -g asterisk -m 0700 \
  /var/spool/asterisk/cmm-books/progress
doorman books list
```

Preparation publishes a catalog only after every chapter and prompt succeeds.
It stores one 16 kHz mono PCM baseline per recording revision (about 110 MiB
per hour) and short prompts. Original recordings stay intact. There is no
per-handset or per-speed full-book copy. Rerunning the same input reuses its
revision directory. Old revisions remain for active calls; reclaim them only
after calls finish and after confirming the current catalog does not refer
to them. Removing a book from the source catalog and preparing again removes
it from new browse sessions without deleting the original recording.

For nonstandard `astspooldir`, pass `--library <astspooldir>/cmm-books/library`
and create the sibling `progress` directory there. Keep the library readable
by Asterisk and progress owned by Asterisk with mode 0700. Neither is served
by an HTTP endpoint. The prepared catalog is the runtime mount list; existing
hosted disk inventory continues to report its configured source roots. This
slice does not yet add ready/mounted status to the cloud inventory contract.

## Install and verify

Install the binary at `/opt/call-me-maybe/bin/doorman` (the generated AGI path),
then run `doorman render`. It produces additive `pjsip_books.conf` and
`extensions_books.conf` fragments. Existing installations can deploy just
these two files without replacing their older handset/voicemail fragments.
In `pjsip.conf`, add `#tryinclude "pjsip_books.conf"` after the handset include;
in `extensions.conf`, add `#tryinclude "extensions_books.conf"` after its
handset include. The shipped examples already contain both. The PJSIP
addition targets `(+type=endpoint)` so it cannot modify the same-named AOR.
Keep these fragments matched to the handset inventory when changing phones.
Check active calls before copying generated files into `/etc/asterisk`;
set the files to asterisk:asterisk, mode 0640. Reload `res_pjsip.so`
for endpoint markers and the dialplan. No Asterisk restart or new daemon is
needed. The house phone directory gains a Books entry when regenerated.
Existing custom dialplans must be checked for a conflicting `*2665` first.

```sh
sudo asterisk -rx 'dialplan show *2665@internal'
sudo -u asterisk doorman books list
```

From a phone, dial `*2665`, interrupt a title with its number, start a chapter,
then try speed, seek, pause and resume after hanging up. Test Books from a
curfewed handset during bedtime: its ordinary curfew gate must still refuse
the call. Trunks and Local channels cannot enter this AGI as a handset; it
requires the PJSIP endpoint to match its generated `CMM_BOOKS_HANDSET` marker.

Each listening call starts a local FFmpeg process with a continuous `atempo`
filter and pipe backpressure. It prepares at most the playing 30-second PCM
window plus one prefetched window, roughly 1.9 MiB total. Asterisk streams the
files and handles DTMF; source offsets are derived from returned sample
positions and speed. Seeking or changing speed cancels and joins the old
process, cleans its temporary directory, and starts at that source offset.
Normal exit/hangup does the same cleanup. A machine crash can leave `play-*`
directories in progress; remove those only with no active Books calls.
These are bounded buffers, not speech synthesis chunks; tempo processing
remains continuous across their boundaries. Actual phone listening is the
final check on Asterisk's file-to-file transitions.

Roll back by restoring the previous binary and generated fragments, then
reload PJSIP and the dialplan. Leave recordings and bookmarks in place. The
underlying phones, inbound routing and source preview server are independent.
