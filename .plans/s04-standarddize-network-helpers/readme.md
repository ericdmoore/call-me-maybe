# s04 · Network helpers — development plan

Doorman is a small brain. The muscle — speech synthesis, transcription, a
language model — can live somewhere else on the network, or in somebody's
cloud, addressed the same way in every case.

**Status:** planned. Nothing started.
Related: `internal/voice`, `docs/TASKS.md` §2 (transcription), issue #7 (LLM
features), `.plans/README.md` (primitives).

---

## The idea

One address format for every helper, whatever it is and wherever it runs:

```
http://localhost:1234
tcp://alpaca.local:10200
http://{{deviceName.local}}/v1/chat
https://api.openai.com/v1/...
```

The Pi answers phones. Anything computationally serious happens elsewhere and
is reached by URL — which means the same binary works for someone with a
spare workstation, someone with an API key, and someone with neither.

---

## What already exists

More than it looks. `internal/voice` was built for exactly this seam:

- A `Renderer` interface that says nothing about transport.
- Five backends: `piper`, `exec`, `elevenlabs`, `openai`, `polly`.
- `Config.Endpoint` on every network backend, so any of them can point at a
  compatible gateway rather than the vendor.
- An `exec` backend — any command taking text on stdin and PCM on stdout —
  which is the escape hatch for anything without a first-class client.

So "TTS somewhere else" is largely solved. **Wyoming is the missing piece**, and
it is a real backend rather than an `exec` shim: JSONL over TCP, one small
client, one more entry in the backends map.

## What is genuinely new

Two capabilities, and they are not more `Renderer`s.

| | Shape | First customer |
|---|---|---|
| **STT** | audio → text | Voicemail transcription (TASKS §2 `externnotify`) |
| **LLM** | text → text | An assistant behind an extension (#7) |

Each gets the same treatment `voice` already has: a narrow interface, an
explicit backends map, `Endpoint` for pointing anywhere, and optional
interfaces for capabilities not every backend has.

Targets named in the sketch:

- **TTS** — Piper via Wyoming (TCP), OpenAI TTS *(already built)*.
- **STT** — Apple `SpeechAnalyzer`/`SpeechTranscriber` behind a small CLI, which
  is an `exec`-shaped backend on macOS; Whisper over HTTP for everyone else.
- **LLM** — Ollama and vLLM locally, speaking either the OpenAI or Anthropic
  wire format; the same clients reach the hosted versions unchanged.

---

## The rule this needs, and does not yet have

**A remote helper may serve an extension. It may never stand in front of the
lobby greeting.**

Invariant 7 says prompts are pre-rendered and the Pi never synthesises on a
call. Remote muscle is fine for build-time work (rendering a pack) and
off-call-path work (transcribing a message that has already been left). It is
not fine between an inbound call and a greeting, because then the phone stops
working whenever the network hiccups — and the failure is a caller listening to
silence, which is the worst symptom this system can produce.

Since LLM features are inherently *on* a call, the boundary has to be explicit:

- Reached by dialling an extension, never by answering the line.
- **Fail closed, never fail slow.** Unreachable helper → a pre-rendered "not
  available right now" and a graceful hang-up. Never a caller waiting on a
  timeout.
- A hard deadline on every call to a helper, shorter than a caller's patience.
- The lobby, the ladder and the bouncer never touch a helper at all.

Written down here because the natural thing to build — an LLM greeting — is
precisely the thing that must not exist.

---

## Milestones

### M1 · Wyoming TTS

The smallest real win, and it uses hardware people already have.

- A `wyoming` backend: JSONL over TCP, one client.
- `tcp://host:port` addressing.
- `doorman pack build --backend wyoming` renders a pack against it.

### M2 · The address format

- One parser for helper URLs, shared by every capability.
- `.local` resolution documented — mDNS on a Pi is a common failure and worth
  a runbook line rather than a support thread.
- Config keys for each capability, in `.env` alongside the existing backend
  credentials.

### M3 · STT

- An `stt` package with the same shape as `voice`.
- HTTP/Whisper backend; `exec` backend for the Apple wrapper.
- Wired to voicemail transcription, which already has its hook designed:
  fire-and-forget, and a dead endpoint must not affect message delivery.

### M4 · LLM

- An `llm` package; OpenAI and Anthropic wire formats, which covers Ollama and
  vLLM locally as well as hosted.
- The extension-only boundary above, enforced by where the code is callable
  from rather than by documentation.

### M5 · Helper setup assistance

See below — deliberately last, and deliberately small.

---

## Helping people set helpers up

The sketch offers two options: emit instructions, or install the CLI on the
muscle machine and drive the install from there.

**Neither, quite. Take the middle.**

Option 2 turns doorman into a fleet configuration tool — host detection,
package managers, WSL2, distro versions — which is a large permanent surface
with no relationship to answering telephones. That is somebody else's product
and it is called Ansible.

Option 1 is honest but weak.

**What earns its place:**

1. `doorman helper install <kind>` emits a **script**, not prose. Copy-and-run
   beats copy-and-interpret, and a script can be read before it is run.
2. `doorman helper check <url>` — **verify the endpoint answers correctly, from
   the Pi.** This is where the value is. Nobody's real problem is installing
   Piper; it is Piper installed and unreachable because of a firewall, a
   `.local` name that will not resolve from the Pi, or a service bound to
   127.0.0.1. A verifier catches all three in one command.
3. For an OS nothing supports, emit context for the operator's own agent to
   work from — which is what `llms.txt` and the published schema already exist
   to make possible.

`check` before `install`, if only one gets built.

---

## Risks

| Risk | Mitigation |
|---|---|
| **A helper ends up on the call path** | The extension-only rule, enforced structurally. The lobby cannot import the helper packages |
| A slow helper becomes a silent caller | Hard deadlines, fail closed, pre-rendered failure prompts |
| Scope creep into config management | `check` yes, `install` as a script, nothing that manages a fleet |
| `.local` resolution fails on the Pi | Documented; `helper check` diagnoses it explicitly |
| Credentials for hosted helpers on the Pi | Same posture as the provider API key: prefer running remote work where the keys already live |
| Four backends × three capabilities becomes a maintenance surface | Same narrow-interface discipline as `voice`; a backend is one method and cannot get the output shape wrong |

---

## Out of scope

Model hosting · GPU provisioning · a helper marketplace · anything that
auto-installs software on a machine the operator has not looked at.
