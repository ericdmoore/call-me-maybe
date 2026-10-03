// swift-tools-version: 6.0
// speechd — Apple's on-device SpeechAnalyzer behind the one HTTP shape
// `doorman phonebook` speaks (OpenAI-compatible /v1/audio/transcriptions).
// Runs on a Mac on the tailnet (macOS 26+); nothing in doorman knows it is
// Apple's engine rather than Whisper. See README.md.
import PackageDescription

let package = Package(
    name: "speechd",
    platforms: [.macOS("26.0")],
    targets: [
        .executableTarget(name: "speechd", path: "Sources/speechd")
    ]
)
