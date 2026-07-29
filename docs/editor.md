# Editor support

`doorman lsp` is a language server (stdio) for `policy.toml` and
`handsets.toml`. Diagnostics come from the exact validator that guards the
daemon's hot reload, so the editor can never disagree with the phone — if
the squiggle is gone, the reload will succeed.

What it does:

- **Diagnostics as you type**: unknown handset/group ids, unknown schedule
  references, duplicate PINs and numbers, sections in the wrong file,
  afterhours with nowhere to send callers, TOML syntax errors (with the
  decoder's exact position). Cross-file: editing `handsets.toml` re-checks
  `policy.toml` and vice versa, using open buffers first and disk siblings
  otherwise.
- **Completions**: handset and group ids inside any `handsets = [...]`,
  schedule ids after `afterhours = "`, day codes in `days = [...]`, and
  mailbox names after `voicemail = "` / `mailbox = "`.

Deliberately absent: hover, formatting (Taplo formats TOML fine), and field
name completion (the `.example` files are the field reference). This server
earns its keep on the cross-file semantics a TOML schema cannot express.

## Neovim (0.10+)

```lua
vim.api.nvim_create_autocmd({ "BufReadPost", "BufNewFile" }, {
  pattern = { "policy.toml", "handsets.toml" },
  callback = function(args)
    vim.lsp.start({
      name = "doorman",
      cmd = { "/opt/call-me-maybe/bin/doorman", "lsp" },
      root_dir = vim.fs.dirname(args.file),
    })
  end,
})
```

Runs happily alongside taplo; you get shape from one and semantics from the
other.

## Helix

`~/.config/helix/languages.toml` — a dedicated language scoped by glob so
ordinary TOML files are untouched:

```toml
[language-server.doorman]
command = "doorman"
args = ["lsp"]

[[language]]
name = "cmm-config"
scope = "source.toml"
file-types = [{ glob = "policy.toml" }, { glob = "handsets.toml" }]
grammar = "toml"
language-servers = ["doorman", "taplo"]
```

## VS Code

VS Code has no built-in generic LSP client; use any "generic LSP" extension
from the marketplace pointed at `doorman lsp` for files matching
`**/policy.toml` and `**/handsets.toml`, or wrap it in a ~30-line extension
with `vscode-languageclient`. (A first-party extension is on the backlog if
this gets real use.)

## Editing over SSH

The server is the same binary the Pi runs, so `nvim` over SSH on the Pi gets
full support with zero extra installation — arguably the primary use case,
given the runbook's whole workflow is "SSH in and edit a file".

## Protocol notes

stdio only, never a port. stdout carries the protocol exclusively; logs go
to stderr. Full-document sync; diagnostics republish on every change of
either file.
