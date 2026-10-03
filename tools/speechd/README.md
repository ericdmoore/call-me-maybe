# speechd — Apple's on-device transcription behind the Whisper endpoint

`doorman phonebook` names the numbers a handset adds with `*88` by posting
the recording to `STT_ENDPOINT`, an OpenAI-compatible
`/v1/audio/transcriptions`. This is that endpoint, served by Apple's
**SpeechAnalyzer** (macOS 26+): on-device, no model to fetch beyond what the
OS installs itself, no key, and quick — a phone-quality 8 kHz clip comes
back in a few hundred milliseconds on Apple silicon. Nothing in doorman
knows it is not Whisper.

```
POST /v1/audio/transcriptions   multipart/form-data, file=<audio>   → {"text": "…"}
GET  /health                                                         → {"ok": true, …}
```

Anything `AVAudioFile` opens works (wav, aiff, m4a, caf); the audio is
converted to the analyzer's own format on the way in.

## Build and run (on the Mac that will serve it)

Needs Xcode 26 or its Command Line Tools.

```bash
cd tools/speechd
swift build -c release
.build/release/speechd --port 9001          # --locale en-US  --bind 0.0.0.0
```

Then on the box, in `.env`:

```
STT_ENDPOINT=http://alpaca:9001/v1/audio/transcriptions
```

(the tailnet name of the Mac; no restart — only `doorman phonebook` reads it).

## As a service

`speechd.plist` is a launchd job that keeps it running and starts it at
login. Edit the two paths, then:

```bash
mkdir -p ~/Library/LaunchAgents
cp speechd.plist ~/Library/LaunchAgents/cc.callmemaybe.speechd.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/cc.callmemaybe.speechd.plist
curl http://localhost:9001/health
```

`launchctl bootout gui/$(id -u)/cc.callmemaybe.speechd` stops it. The first
transcription in a locale installs that locale's assets (a one-time, OS-managed
download); the service warms the engine at start so the first real request
does not pay for it.

## Where it sits

It is a helper addressed by URL, exactly as `.plans/s04` describes: it may
serve off-call work and never stands in front of the lobby. The box reaches
it over the tailnet; it listens on every interface by default (`--bind
127.0.0.1` to keep it local), and it has no authentication — it is meant
for a private network, and the only thing it ever receives is a few seconds
of someone saying a name.
