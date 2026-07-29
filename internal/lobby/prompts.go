package lobby

// Prompts are pre-rendered WAVs shipped with the repo (see prompts/build.sh),
// not runtime TTS. The Pi does no synthesis: playback is a file read, which is
// why the lobby can answer and start talking in well under a second — and why
// a broken speech service can never make the house phone unreachable.
//
// These names are the contract with prompts/manifest.json — add in both
// places and re-run the build.

type Prompt string

const (
	PromptWelcomeKnown     Prompt = "welcome-known"
	PromptLobbyGreeting    Prompt = "lobby-greeting"
	PromptInvalidExtension Prompt = "invalid-extension"
	PromptGoodDay          Prompt = "good-day"
	PromptNoAnswer         Prompt = "no-answer"
	PromptConnecting       Prompt = "connecting"
)

// Prompts maps prompt names to ARI media URIs under a configured prefix.
type Prompts struct{ prefix string }

func NewPrompts(mediaPrefix string) *Prompts {
	return &Prompts{prefix: mediaPrefix}
}

// URI renders e.g. "sound:call-me-maybe/good-day". The prefix is an Asterisk
// media path under /var/lib/asterisk/sounds/, not a filesystem path.
func (p *Prompts) URI(name Prompt) string {
	return "sound:" + p.prefix + "/" + string(name)
}
