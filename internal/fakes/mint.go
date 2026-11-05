package fakes

import (
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Seen is one request the fake mint received.
type Seen struct {
	Method, Path, Query, SNI string
	Header                   http.Header
	Body                     []byte
}

// Mint is a fake Cashu mint over TLS. It serves the NUT-06/02/07 endpoints
// under "/" and under "/Bitcoin" (mints with a path prefix), plus test hooks.
type Mint struct {
	Addr  string
	Spent map[string]bool // Ys reported as SPENT

	Conns atomic.Int64 // TCP connections accepted

	mu   sync.Mutex
	seen []Seen
	srv  *http.Server
}

// StartMint serves on 127.0.0.1:0 with a certificate for names.
func StartMint(ca *CA, names ...string) *Mint {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{ca.Leaf(names...)}})
	if err != nil {
		panic(err)
	}
	m := &Mint{Addr: ln.Addr().String(), Spent: map[string]bool{}}
	m.srv = &http.Server{Handler: m, ConnState: func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			m.Conns.Add(1)
		}
	}}
	go m.srv.Serve(ln)
	return m
}

func (m *Mint) Close() { m.srv.Close() }

// Requests returns what the mint has received so far.
func (m *Mint) Requests() []Seen {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Seen(nil), m.seen...)
}

// Last is the most recent request.
func (m *Mint) Last() Seen {
	r := m.Requests()
	if len(r) == 0 {
		return Seen{}
	}
	return r[len(r)-1]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (m *Mint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	s := Seen{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: body}
	if r.TLS != nil {
		s.SNI = r.TLS.ServerName
	}
	m.mu.Lock()
	m.seen = append(m.seen, s)
	m.mu.Unlock()

	path := strings.TrimPrefix(r.URL.Path, "/Bitcoin")
	switch path {
	case "/v1/info":
		name := "Fake mint"
		if path != r.URL.Path {
			name = "Fake mint (Bitcoin)"
		}
		writeJSON(w, 200, map[string]any{"name": name, "version": "fakemint/0.1", "pubkey": "02" + strings.Repeat("ab", 32),
			"nuts": map[string]any{"4": map[string]any{"methods": []any{}, "disabled": false}, "7": map[string]any{"supported": true}}})
	case "/v1/keysets":
		writeJSON(w, 200, map[string]any{"keysets": []any{map[string]any{"id": "00ad268c4d1f5826", "unit": "sat", "active": true, "input_fee_ppk": 0}}})
	case "/v1/checkstate":
		var req struct {
			Ys []string `json:"Ys"`
		}
		if r.Method != http.MethodPost || json.Unmarshal(body, &req) != nil {
			writeJSON(w, 400, map[string]any{"detail": "bad checkstate request", "code": 10000})
			return
		}
		states := []any{}
		for _, y := range req.Ys {
			st := "UNSPENT"
			if m.Spent[y] {
				st = "SPENT"
			}
			states = append(states, map[string]any{"Y": y, "state": st, "witness": nil})
		}
		writeJSON(w, 200, map[string]any{"states": states})
	case "/v1/cookie":
		http.SetCookie(w, &http.Cookie{Name: "track", Value: "you"})
		w.Header().Set("Server", "fakemint")
		w.Header().Set("Alt-Svc", `h3=":443"`)
		writeJSON(w, 200, map[string]any{"ok": true})
	case "/v1/big":
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if r.URL.Query().Get("chunked") == "" {
			w.Header().Set("Content-Length", strconv.Itoa(n))
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		chunk := []byte(strings.Repeat("x", 4096))
		for n > 0 {
			k := min(n, len(chunk))
			w.Write(chunk[:k])
			n -= k
		}
	case "/v1/slow":
		d, _ := time.ParseDuration(r.URL.Query().Get("d"))
		time.Sleep(d)
		writeJSON(w, 200, map[string]any{"ok": true})
	case "/v1/fail":
		writeJSON(w, 400, map[string]any{"detail": "Token already spent.", "code": 11001})
	case "/v1/ws":
		m.serveEcho(w, r)
	default:
		writeJSON(w, 404, map[string]any{"detail": "not found", "code": 0})
	}
}

// serveEcho completes a WebSocket handshake and then echoes raw bytes; the
// gateway only splices streams, so framing does not matter for tests.
func (m *Mint) serveEcho(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		writeJSON(w, 426, map[string]any{"detail": "upgrade required"})
		return
	}
	h := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(h[:]) + "\r\nSet-Cookie: ws=1\r\n\r\n")
	brw.Flush()
	buf := make([]byte, 4096)
	for {
		n, err := brw.Read(buf)
		if n > 0 {
			conn.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}
