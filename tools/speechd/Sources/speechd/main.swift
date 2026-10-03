// speechd: audio in, text out, over the OpenAI-compatible transcription
// endpoint, using Apple's on-device SpeechAnalyzer (macOS 26). One binary,
// no model download beyond what the OS fetches itself, no key.
//
//   speechd [--port 9001] [--locale en-US] [--bind 0.0.0.0]
//
//   POST /v1/audio/transcriptions   multipart: file=<audio>  → {"text": "…"}
//   GET  /health                                              → {"ok": true}
//
// Asterisk's *88 recordings are 8 kHz mono WAV; the analyzer wants its own
// format, so the file is decoded with AVAudioFile and converted on the way
// in. Anything AVAudioFile can open (wav, aiff, m4a, caf) works.

import AVFoundation
import Foundation
import Network
import Speech

// MARK: - options

struct Options: Sendable {
    var port: UInt16 = 9001
    var localeID = "en-US"
    var bindHost = "0.0.0.0"
}

func parseOptions() -> Options {
    var o = Options()
    var args = CommandLine.arguments.dropFirst().makeIterator()
    while let a = args.next() {
        switch a {
        case "--port": o.port = UInt16(args.next() ?? "") ?? o.port
        case "--locale": o.localeID = args.next() ?? o.localeID
        case "--bind": o.bindHost = args.next() ?? o.bindHost
        case "-h", "--help":
            print("usage: speechd [--port 9001] [--locale en-US] [--bind 0.0.0.0]")
            exit(0)
        default:
            FileHandle.standardError.write("speechd: unknown argument \(a)\n".data(using: .utf8)!)
            exit(2)
        }
    }
    return o
}

nonisolated(unsafe) let opts = parseOptions()

// MARK: - transcription

/// One transcriber, reused: asset installation is the slow part and only
/// has to happen once per locale.
actor Engine {
    let locale: Locale
    private var ready = false

    init(locale: Locale) { self.locale = locale }

    private func prepare(_ transcriber: SpeechTranscriber) async throws {
        if ready { return }
        if let req = try await AssetInventory.assetInstallationRequest(supporting: [transcriber]) {
            try await req.downloadAndInstall()
        }
        ready = true
    }

    func transcribe(fileURL: URL) async throws -> String {
        let transcriber = SpeechTranscriber(locale: locale, preset: .transcription)
        try await prepare(transcriber)
        let analyzer = SpeechAnalyzer(modules: [transcriber])

        // Collect results as they arrive; only final ones count.
        let collector = Task<String, Error> {
            var text = ""
            for try await result in transcriber.results where result.isFinal {
                let piece = String(result.text.characters)
                if !piece.isEmpty {
                    text += (text.isEmpty ? "" : " ") + piece
                }
            }
            return text
        }

        let file = try AVAudioFile(forReading: fileURL)
        guard let wanted = await SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith: [transcriber]) else {
            throw NSError(domain: "speechd", code: 1, userInfo: [NSLocalizedDescriptionKey: "no compatible audio format"])
        }
        let (stream, continuation) = AsyncStream<AnalyzerInput>.makeStream()
        // Decode and convert the whole file up front: *88 clips are seconds long.
        let frames = AVAudioFrameCount(file.length)
        guard let raw = AVAudioPCMBuffer(pcmFormat: file.processingFormat, frameCapacity: max(frames, 1)) else {
            throw NSError(domain: "speechd", code: 2, userInfo: [NSLocalizedDescriptionKey: "cannot allocate audio buffer"])
        }
        try file.read(into: raw)
        let converted: AVAudioPCMBuffer
        if raw.format == wanted {
            converted = raw
        } else {
            guard let conv = AVAudioConverter(from: raw.format, to: wanted) else {
                throw NSError(domain: "speechd", code: 3, userInfo: [NSLocalizedDescriptionKey: "cannot convert audio"])
            }
            let ratio = wanted.sampleRate / raw.format.sampleRate
            let outCap = AVAudioFrameCount(Double(raw.frameLength) * ratio) + 1024
            guard let out = AVAudioPCMBuffer(pcmFormat: wanted, frameCapacity: outCap) else {
                throw NSError(domain: "speechd", code: 2, userInfo: [NSLocalizedDescriptionKey: "cannot allocate audio buffer"])
            }
            var consumed = false
            var convError: NSError?
            conv.convert(to: out, error: &convError) { _, status in
                if consumed { status.pointee = .endOfStream; return nil }
                consumed = true
                status.pointee = .haveData
                return raw
            }
            if let convError { throw convError }
            converted = out
        }
        continuation.yield(AnalyzerInput(buffer: converted))
        continuation.finish()

        let last = try await analyzer.analyzeSequence(stream)
        try await analyzer.finalizeAndFinish(through: last ?? CMTime.zero)
        return try await collector.value
    }
}

nonisolated(unsafe) let engine = Engine(locale: Locale(identifier: opts.localeID))

// MARK: - a very small HTTP/1.1 server (one request per connection)

struct Request {
    var method = ""
    var path = ""
    var headers: [String: String] = [:]
    var body = Data()
}

func parseMultipartFile(_ body: Data, contentType: String) -> (name: String, data: Data)? {
    guard let bRange = contentType.range(of: "boundary=") else { return nil }
    var boundary = String(contentType[bRange.upperBound...])
    if let semi = boundary.firstIndex(of: ";") { boundary = String(boundary[..<semi]) }
    boundary = boundary.trimmingCharacters(in: CharacterSet(charactersIn: "\" "))
    let delim = Data(("--" + boundary).utf8)
    var cursor = body.startIndex
    while let start = body.range(of: delim, in: cursor..<body.endIndex) {
        var partStart = start.upperBound
        if body[partStart...].starts(with: Data("--".utf8)) { break }
        if body[partStart...].starts(with: Data("\r\n".utf8)) { partStart += 2 }
        guard let headEnd = body.range(of: Data("\r\n\r\n".utf8), in: partStart..<body.endIndex) else { break }
        let head = String(decoding: body[partStart..<headEnd.lowerBound], as: UTF8.self)
        let dataStart = headEnd.upperBound
        guard let next = body.range(of: delim, in: dataStart..<body.endIndex) else { break }
        var dataEnd = next.lowerBound
        if dataEnd >= dataStart + 2 { dataEnd -= 2 } // the CRLF before the boundary
        if head.lowercased().contains("name=\"file\"") {
            var filename = "audio.wav"
            if let fr = head.range(of: "filename=\"") {
                let rest = head[fr.upperBound...]
                if let q = rest.firstIndex(of: "\"") { filename = String(rest[..<q]) }
            }
            return (filename, body[dataStart..<dataEnd])
        }
        cursor = next.lowerBound
    }
    return nil
}

func respond(_ conn: NWConnection, status: Int, reason: String, json: String) {
    let body = Data(json.utf8)
    let head = "HTTP/1.1 \(status) \(reason)\r\nContent-Type: application/json\r\nContent-Length: \(body.count)\r\nConnection: close\r\n\r\n"
    conn.send(content: Data(head.utf8) + body, completion: .contentProcessed { _ in conn.cancel() })
}

func jsonString(_ s: String) -> String {
    let escaped = s.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "\"", with: "\\\"")
        .replacingOccurrences(of: "\n", with: "\\n").replacingOccurrences(of: "\r", with: "\\r")
    return "\"\(escaped)\""
}

func handle(_ req: Request, on conn: NWConnection) {
    switch (req.method, req.path.split(separator: "?").first.map(String.init) ?? req.path) {
    case ("GET", "/health"):
        respond(conn, status: 200, reason: "OK", json: "{\"ok\":true,\"engine\":\"SpeechAnalyzer\",\"locale\":\(jsonString(opts.localeID))}")
    case ("POST", "/v1/audio/transcriptions"), ("POST", "/inference"):
        guard let ct = req.headers["content-type"], ct.lowercased().contains("multipart/form-data"),
              let part = parseMultipartFile(req.body, contentType: ct) else {
            respond(conn, status: 400, reason: "Bad Request", json: "{\"error\":\"multipart form with a file part is required\"}")
            return
        }
        let ext = (part.name as NSString).pathExtension.isEmpty ? "wav" : (part.name as NSString).pathExtension
        let tmp = FileManager.default.temporaryDirectory.appendingPathComponent("speechd-\(UUID().uuidString).\(ext)")
        do {
            try part.data.write(to: tmp)
        } catch {
            respond(conn, status: 500, reason: "Internal Server Error", json: "{\"error\":\"cannot write temp file\"}")
            return
        }
        Task {
            defer { try? FileManager.default.removeItem(at: tmp) }
            do {
                let text = try await engine.transcribe(fileURL: tmp)
                respond(conn, status: 200, reason: "OK", json: "{\"text\":\(jsonString(text))}")
            } catch {
                let msg = String(describing: error)
                FileHandle.standardError.write("speechd: transcription failed: \(msg)\n".data(using: .utf8)!)
                respond(conn, status: 500, reason: "Internal Server Error", json: "{\"error\":\(jsonString(msg))}")
            }
        }
    default:
        respond(conn, status: 404, reason: "Not Found", json: "{\"error\":\"not found\"}")
    }
}

func serve(_ conn: NWConnection) {
    var buffer = Data()
    func readMore() {
        conn.receive(minimumIncompleteLength: 1, maximumLength: 1 << 20) { data, _, isComplete, error in
            if let data { buffer.append(data) }
            if error != nil { conn.cancel(); return }
            if let headEnd = buffer.range(of: Data("\r\n\r\n".utf8)) {
                let headText = String(decoding: buffer[..<headEnd.lowerBound], as: UTF8.self)
                var lines = headText.components(separatedBy: "\r\n")
                let requestLine = lines.removeFirst().split(separator: " ")
                var req = Request()
                req.method = requestLine.count > 0 ? String(requestLine[0]) : ""
                req.path = requestLine.count > 1 ? String(requestLine[1]) : "/"
                for l in lines {
                    if let c = l.firstIndex(of: ":") {
                        req.headers[l[..<c].lowercased()] = l[l.index(after: c)...].trimmingCharacters(in: .whitespaces)
                    }
                }
                let length = Int(req.headers["content-length"] ?? "0") ?? 0
                let bodyStart = headEnd.upperBound
                if buffer.count - bodyStart >= length {
                    req.body = buffer[bodyStart..<(bodyStart + length)]
                    handle(req, on: conn)
                    return
                }
                if length > 64 << 20 {
                    respond(conn, status: 413, reason: "Payload Too Large", json: "{\"error\":\"too large\"}")
                    return
                }
            }
            if isComplete { conn.cancel(); return }
            readMore()
        }
    }
    conn.start(queue: .global())
    readMore()
}

let params = NWParameters.tcp
params.allowLocalEndpointReuse = true
let listener = try NWListener(using: params, on: NWEndpoint.Port(rawValue: opts.port)!)
listener.newConnectionHandler = { conn in serve(conn) }
listener.stateUpdateHandler = { state in
    switch state {
    case .ready:
        print("speechd: SpeechAnalyzer (\(opts.localeID)) on http://\(opts.bindHost):\(opts.port)/v1/audio/transcriptions")
    case .failed(let err):
        FileHandle.standardError.write("speechd: listener failed: \(err)\n".data(using: .utf8)!)
        exit(1)
    default: break
    }
}
listener.start(queue: .main)
// Warm the engine so the first real request does not pay for asset installation.
Task {
    do {
        let silence = FileManager.default.temporaryDirectory.appendingPathComponent("speechd-warm.wav")
        let fmt = AVAudioFormat(standardFormatWithSampleRate: 16000, channels: 1)!
        let f = try AVAudioFile(forWriting: silence, settings: fmt.settings)
        let buf = AVAudioPCMBuffer(pcmFormat: fmt, frameCapacity: 16000)!
        buf.frameLength = 16000
        try f.write(from: buf)
        _ = try await engine.transcribe(fileURL: silence)
        try? FileManager.default.removeItem(at: silence)
        print("speechd: engine ready")
    } catch {
        // A silent clip has nothing to say; the assets are installed either
        // way, which is all the warm-up is for.
        print("speechd: engine ready")
    }
}
dispatchMain()
