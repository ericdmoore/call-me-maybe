# The CLI on the site

A proposal for `callmemaybe.cc/doorman/`: one page per subcommand saying what
it does, when you would reach for it, and how to call it — for early adopters
and the technical crowd, with a Markdown twin of every page for the models that
arrive by way of `llms.txt`. `/cli` redirects there.

The reference half of each page is generated from the binary. The prose half
is written by hand. Keeping those two apart is the whole design.

## Where the subcommand surface lives today

Four copies, all typed separately, and nothing asserts they agree:

| Copy | Where |
|---|---|
| the dispatch | the `switch` in `cmd/doorman/main.go` |
| `doorman help` | the 237-line `usage` constant beside it |
| the man page | `SUBCOMMANDS` in `docs/doorman.1` |
| the orientation file | the command block near the top of `llms.txt` |

Flags are defined inline inside each subcommand's run function, so there is no
way to list a command's flags without executing the command. There is no
`doorman help <command>`; `-h` on a subcommand prints the Go flag defaults and
nothing else. Twenty subcommands, fourteen files with a `flag.NewFlagSet`.

A site page generated from any of this would have to scrape prose, which only
adds a fifth copy.

The schema side already solved the same problem. `doorman schema` emits JSON;
`make site-assets` writes it into `site/public/schema/`; CI's *site assets are
current* step regenerates and diffs, so a copy cannot go stale without failing
a PR. That is the shape to copy — and it is why the config surface has one
source and the command surface has four.

## The proposal

### 1. A command registry in the binary

One table, one entry per subcommand:

- name, one-line summary, synopsis
- the flags: name, default, help — introspected, not retyped
- what it needs: the env vars and files it reads, whether it needs the daemon
  running, whether it is safe to run on a workstation with a copy of the TOMLs
- exit codes, where they mean something (`balance` exits 1 under threshold and
  3 when a provider could not be checked)
- examples, each with one line of why

Flags become introspectable by splitting each subcommand's flag definition
from its run: a function that builds and returns the `*flag.FlagSet`, which
the run calls. `FlagSet.VisitAll` then yields every flag's name, default and
help with nothing executed. Twenty files, mechanical, no behaviour change.

Defaults in the JSON are the literal text the code states — `(default
$TRUNKS_PATH or ./trunks.toml)` — never an expanded variable and never a path
from the machine that ran the generator. Generation runs in a clean
environment, which is what CI is.

### 2. `doorman help --json`, and `doorman help <command>`

The JSON is the source. Plain `doorman help` renders from the registry and the
`usage` constant is deleted, otherwise the constant becomes the fifth copy.
`doorman help check` prints one command in full, which is the version an
operator on the Pi actually sees; the man page is installed too, but a
per-command help that matches it costs nothing once the registry exists.

`doorman schema cli` is the alternative spelling and matches the existing
verb. Either is fine; pick one and never offer both.

### 3. A test that the dispatch and the registry agree

Both directions, in `cmd/doorman/main_test.go`: every `case` in the switch is
registered, and every registered command dispatches. This is the gate that is
missing today, and it is the one that makes the rest trustworthy.

### 4. On the site: generated reference, written prose

`make site-assets` writes `site/public/cli/commands.json`, exactly as it writes
the schemas, and the existing CI diff covers it.

The prose lives in a `commands` content collection — one Markdown file per
subcommand under `site/src/content/commands/` — carrying the *when would I
need this* and the worked examples, the same split the `features` collection
already uses: frontmatter is data, body is prose. The page joins the two at
build time.

Add the guard `providers.astro` applies to its comparison table: the build
fails if the JSON names a command with no prose file, or a prose file names a
command the binary no longer has. A new subcommand then fails CI twice until
it is documented, which is the behaviour wanted.

**No half-generated page.** If six commands show introspected flags and
fourteen show paragraphs, a reader trusts the wrong six. Flags for every
command or for none in the first cut; the registry makes "every" the easy
option.

The register is different from the rest of the site — a reference, not a
lobby — but it keeps the frame: `Base` and `HomeNav`, the Deco type for
headings, monospace for the rest, and an anchor on every flag so a URL can
point at `-ring` and nothing else. Make it searchable before making it pretty.

### 5. URLs

- `/doorman/` — the index: every command, its summary, and what it needs
- `/doorman/<command>/` — one page per subcommand, mirroring
  `/features/<id>/`
- `/cli` and `/cli/` — 301 to `/doorman/`, from a `_redirects` file in
  `site/public/`. Workers static assets honour it; `astro preview` does not,
  so confirm under `npm run cf:preview` as the site readme already advises.

The man page's `SEE ALSO` names the index URL, and `doorman help <command>`
prints the page URL on its last line.

### 6. The Markdown twin

Once the data is structured, a Markdown rendering per page is one Astro
endpoint — `src/pages/doorman/[command].md.ts` — producing the JSON fields plus
the prose body as plain Markdown. `llms.txt` links the index and drops its
hand-typed command block. Same source, two renderings, nothing to drift.

### 7. Examples that run

Examples in the registry carry a `readonly` mark. CI executes the read-only
ones — `schema`, `e164`, `version`, `check` against `examples/` — the way it
already runs `doorman check` and `doorman init` on the example files. A
generated example that is also an executed example is trustworthy rather than
merely consistent.

### 8. Later: the man page and llms.txt from the same source

With the registry in place, `SUBCOMMANDS` in `docs/doorman.1` and the command
block in `llms.txt` can both be rendered from the JSON. Not in the first cut:
the man page carries prose the registry will not, and the schema tests already
show how to assert that `llms.txt` names what it must. Start with a test that
every registered command appears in both; render them later if the test keeps
failing.

## What does not change

- The deploy. CI forces regeneration at PR time; the pages go live on the next
  `npm run deploy` from `site/`, alongside `install.sh` and `llms.txt`, as now.
- The daemon. The registry is read by `help` and by the generator; nothing on
  the call path touches it.
- The invariants. Nothing in the JSON is a secret: flag defaults are source
  text, examples use the `555-01xx` numbers, and generation never reads an
  operator's `.env`.

## Order of work

The registry and its test first — they are useful on their own and make
`doorman help <command>` possible. Then `site-assets` and the content
collection. Then the redirect and the Markdown twin. The backlog entry in
[`TASKS.md`](TASKS.md#cli-reference-pages--one-source-for-help-the-man-page-llmstxt-and-the-site) carries the acceptance criteria.
