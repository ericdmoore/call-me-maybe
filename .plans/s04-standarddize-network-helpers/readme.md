Network Helpers
=================

Call Me Maybe - Doorman assumes a clever but small brain

- The Muscle" can be local but does not have to be local
    - `http://localhost:1234` 
    - `tcp://device.name:123`
    - `http://{{deviceName.local}}/v1/chat/etc`
    - `https://api.openai.com/v1/etc`

- Supported TTS processes:
    - Piper via Wyoming (TCP)
    - OpenAI TTS

- Supported SST process:
    - new Apple SpeechAnalyzer + SpeechTranscriber (need to wrap these in some CLI that can pick up jobs and return)

- Supported LLM APIs
    - "Local LLMs" over 'Ollama', 'vLLM',  supported specs: "OpenAI" | "Anthropic"
    - True Remote APIs too - similar api calls


I am imaginging the same doormanCLI can help people install the other wrappers and deps on remote computers:
- seems like there are two options
1. CLI spits out instructions that the admin has to copy/paste into the new system and hope it runs
2. CLI is installed on the "muscle system" too - and used basically to contexturalize the install process
    types:
    - Distrod-Linux-Version
    - MacOS
    - using WSL2
    Based on the host info 
    - the CLI could drive questions and subshell commands to get the installs compelted
    - if the CLI does not natively support driving the isntall for the isoteric OS.Version then it can spit out some prose. Tell your agents you are trying to intall.... but ... on this machine..etc 