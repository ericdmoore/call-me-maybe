package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Server speaks LSP over a reader/writer pair — in practice stdin/stdout,
// which is why doorman's lsp mode must never print anything else to stdout.
type Server struct {
	in  *bufio.Reader
	out io.Writer
	log *slog.Logger

	mu   sync.Mutex
	docs map[string]string // uri -> current text
}

func NewServer(in io.Reader, out io.Writer, log *slog.Logger) *Server {
	return &Server{
		in:   bufio.NewReader(in),
		out:  out,
		log:  log,
		docs: make(map[string]string),
	}
}

type request struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params"`
}

// Run processes messages until exit or EOF.
func (s *Server) Run() error {
	for {
		req, err := s.read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		switch req.Method {
		case "initialize":
			s.reply(req.ID, map[string]any{
				"capabilities": map[string]any{
					"textDocumentSync": 1, // full document sync
					"completionProvider": map[string]any{
						"triggerCharacters": []string{`"`, "[", ",", " "},
					},
				},
				"serverInfo": map[string]any{"name": "doorman", "version": "0.4.1"},
			})
		case "initialized", "textDocument/didSave", "$/setTrace":
			// nothing to do
		case "shutdown":
			s.reply(req.ID, nil)
		case "exit":
			return nil

		case "textDocument/didOpen":
			var p struct {
				TextDocument struct {
					URI  string `json:"uri"`
					Text string `json:"text"`
				} `json:"textDocument"`
			}
			if json.Unmarshal(req.Params, &p) == nil {
				s.setDoc(p.TextDocument.URI, p.TextDocument.Text)
				s.publishAll()
			}

		case "textDocument/didChange":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
				ContentChanges []struct {
					Text string `json:"text"`
				} `json:"contentChanges"`
			}
			if json.Unmarshal(req.Params, &p) == nil && len(p.ContentChanges) > 0 {
				// Full sync: the last change is the whole document.
				s.setDoc(p.TextDocument.URI, p.ContentChanges[len(p.ContentChanges)-1].Text)
				s.publishAll()
			}

		case "textDocument/didClose":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			if json.Unmarshal(req.Params, &p) == nil {
				s.mu.Lock()
				delete(s.docs, p.TextDocument.URI)
				s.mu.Unlock()
				s.notify("textDocument/publishDiagnostics", map[string]any{
					"uri": p.TextDocument.URI, "diagnostics": []Diagnostic{},
				})
			}

		case "textDocument/completion":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
				Position Position `json:"position"`
			}
			if json.Unmarshal(req.Params, &p) != nil {
				s.reply(req.ID, []CompletionItem{})
				continue
			}
			s.reply(req.ID, s.complete(p.TextDocument.URI, p.Position))

		default:
			// Requests must be answered even when unhandled; notifications
			// are silently fine to ignore.
			if req.ID != nil {
				s.replyError(req.ID, -32601, "method not found: "+req.Method)
			}
		}
	}
}

// ── Document handling ────────────────────────────────────────────────────

func (s *Server) setDoc(uri, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[uri] = text
}

// pair resolves the policy/handsets texts for analysis: open buffers first,
// then siblings on disk — so edits in one file immediately re-validate the
// other, and a lone open file still sees its counterpart.
func (s *Server) pair(uri string) docSet {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := uriToPath(uri)
	dir := filepath.Dir(path)

	find := func(kind DocKind, sibling string) (string, string, bool) {
		for u, text := range s.docs {
			if KindOf(uriToPath(u)) == kind {
				return u, text, true
			}
		}
		diskPath := filepath.Join(dir, sibling)
		if data, err := os.ReadFile(diskPath); err == nil {
			return pathToURI(diskPath), string(data), true
		}
		return "", "", false
	}

	var d docSet
	pURI, pText, pOK := find(KindPolicy, "policy.toml")
	hURI, hText, hOK := find(KindHandsets, "handsets.toml")
	tURI, tText, tOK := find(KindTrunks, "trunks.toml")

	if pOK {
		// Handsets open alone with no policy anywhere: pairing with an
		// empty-but-valid-enough policy is worse than pairing with nothing, so
		// use an empty policy and let attribution put inventory problems in
		// the handsets doc.
		d.policyURI, d.policyText = pURI, pText
	}
	if hOK {
		d.handsetsURI, d.handsets = hURI, &hText
	}
	if tOK {
		d.trunksURI, d.trunks = tURI, &tText
	}
	return d
}

// docSet is the config files in play for one analysis: the policy/handsets
// pair the cross-file rules need, plus the provider inventory, which is linted
// on its own and referenced from the policy.
type docSet struct {
	policyURI   string
	policyText  string
	handsetsURI string
	handsets    *string
	trunksURI   string
	trunks      *string
}

func (s *Server) publishAll() {
	// Analyse once, publish per document. Which URIs get published: any of
	// the files that is actually open.
	s.mu.Lock()
	uris := make([]string, 0, len(s.docs))
	for u := range s.docs {
		uris = append(uris, u)
	}
	s.mu.Unlock()
	if len(uris) == 0 {
		return
	}

	d := s.pair(uris[0])
	pDiags, hDiags := AnalyseWithTrunks(d.policyText, d.handsets, d.trunks)

	published := map[string]bool{}
	publish := func(uri string, diags []Diagnostic) {
		if uri == "" {
			return
		}
		s.notify("textDocument/publishDiagnostics", map[string]any{"uri": uri, "diagnostics": nonNil(diags)})
		published[uri] = true
	}
	publish(d.policyURI, pDiags)
	publish(d.handsetsURI, hDiags)
	if d.trunks != nil {
		publish(d.trunksURI, AnalyseTrunks(*d.trunks))
	}
	// Clear any other open doc we did not analyse into.
	for _, u := range uris {
		if !published[u] {
			s.notify("textDocument/publishDiagnostics", map[string]any{"uri": u, "diagnostics": []Diagnostic{}})
		}
	}
}

func nonNil(d []Diagnostic) []Diagnostic {
	if d == nil {
		return []Diagnostic{}
	}
	return d
}

func (s *Server) complete(uri string, pos Position) []CompletionItem {
	s.mu.Lock()
	text, ok := s.docs[uri]
	s.mu.Unlock()
	if !ok {
		return []CompletionItem{}
	}

	d := s.pair(uri)
	model := BuildModelWith(d.policyText, d.handsets, d.trunks)

	lines := strings.Split(text, "\n")
	if pos.Line < 0 || pos.Line >= len(lines) {
		return []CompletionItem{}
	}
	out := Complete(model, lines[pos.Line], pos.Character)
	if out == nil {
		return []CompletionItem{}
	}
	return out
}

// ── Wire format ──────────────────────────────────────────────────────────

func (s *Server) read() (*request, error) {
	contentLength := 0
	for {
		line, err := s.in.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if v, ok := strings.CutPrefix(line, "Content-Length: "); ok {
			contentLength, err = strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return nil, fmt.Errorf("bad Content-Length: %w", err)
			}
		}
	}
	if contentLength <= 0 {
		return nil, fmt.Errorf("missing Content-Length")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(s.in, body); err != nil {
		return nil, err
	}
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

func (s *Server) send(payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		s.log.Error("lsp marshal failed", "err", err)
		return
	}
	fmt.Fprintf(s.out, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

func (s *Server) reply(id *json.RawMessage, result any) {
	if id == nil {
		return
	}
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *Server) replyError(id *json.RawMessage, code int, message string) {
	s.send(map[string]any{"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message}})
}

func (s *Server) notify(method string, params any) {
	s.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// ── URI helpers ──────────────────────────────────────────────────────────

func uriToPath(uri string) string {
	trimmed := strings.TrimPrefix(uri, "file://")
	if unescaped, err := url.PathUnescape(trimmed); err == nil {
		return unescaped
	}
	return trimmed
}

func pathToURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return "file://" + abs
}
