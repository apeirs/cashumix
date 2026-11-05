package cashu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// UserAgent is the one User-Agent cashumix sends, from the CLI and the
// gateway alike, so its users all look the same to a mint.
const UserAgent = "cashumix"

// MaxResponse bounds how much of a mint response the client reads.
const MaxResponse = 8 << 20

// CheckStateBatch is how many Ys go into one /v1/checkstate request.
const CheckStateBatch = 100

// NormalizeMint turns "mint.example.com", "mint.example.com/Bitcoin" or
// "https://mint.example.com/Bitcoin/" into an https base URL without a
// trailing slash. Only https is accepted: plain http would be readable at the
// mixnet exit.
func NormalizeMint(s string) (*url.URL, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty mint URL")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	if real, ok := UnwrapGatewayURL(s); ok {
		s = real
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("bad mint URL: %w", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("mint URL %q: only https mints are supported (http would be cleartext at the mixnet exit)", s)
	}
	if u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("bad mint URL %q", s)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

// UnwrapGatewayURL turns a mint URL that points at a local cashumix gateway,
// such as http://127.0.0.1:3338/testnut.cashu.space, back into the mint's
// own URL, https://testnut.cashu.space. Wallets that use the gateway put
// that local URL into the tokens they send.
func UnwrapGatewayURL(s string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme != "http" {
		return "", false
	}
	switch h := u.Hostname(); {
	case h == "localhost":
	case net.ParseIP(h) != nil && net.ParseIP(h).IsLoopback():
	default:
		return "", false
	}
	p := strings.TrimPrefix(u.Path, "/")
	p = strings.TrimPrefix(strings.TrimPrefix(p, "https://"), "https:/")
	first, _, _ := strings.Cut(p, "/")
	if !strings.Contains(first, ".") || u.RawQuery != "" {
		return "", false
	}
	return "https://" + strings.TrimRight(p, "/"), true
}

// MintError is an error answer from a mint (NUT-00 error format).
type MintError struct {
	Status int
	Code   int    `json:"code"`
	Detail string `json:"detail"`
}

func (e *MintError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("mint answered HTTP %d", e.Status)
	}
	if e.Code != 0 {
		return fmt.Sprintf("mint error %d (HTTP %d): %s", e.Code, e.Status, e.Detail)
	}
	return fmt.Sprintf("mint error (HTTP %d): %s", e.Status, e.Detail)
}

// Client talks to a mint's read-only endpoints. HTTP decides the route:
// cashumix gives it a transport that dials through the mixnet.
type Client struct {
	HTTP *http.Client
}

// Call is one request with its timing.
type Call struct {
	Method, URL string
	Status      int
	Elapsed     time.Duration
}

func (c *Client) do(ctx context.Context, method string, mint *url.URL, path string, in, out any) (Call, error) {
	u := *mint
	u.Path = mint.Path + path
	call := Call{Method: method, URL: u.String()}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return call, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return call, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	t0 := time.Now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		call.Elapsed = time.Since(t0)
		return call, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
	call.Elapsed = time.Since(t0)
	call.Status = resp.StatusCode
	if err != nil {
		return call, err
	}
	if len(raw) > MaxResponse {
		return call, fmt.Errorf("%s: response larger than %d bytes", u.String(), MaxResponse)
	}
	if resp.StatusCode != http.StatusOK {
		me := &MintError{Status: resp.StatusCode}
		_ = json.Unmarshal(raw, me)
		return call, me
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return call, fmt.Errorf("%s: bad JSON from mint: %w", u.String(), err)
	}
	return call, nil
}

// Info is the NUT-06 mint information document.
type Info struct {
	Name            string                     `json:"name"`
	Pubkey          string                     `json:"pubkey"`
	Version         string                     `json:"version"`
	Description     string                     `json:"description"`
	DescriptionLong string                     `json:"description_long,omitempty"`
	Contact         json.RawMessage            `json:"contact,omitempty"`
	Motd            string                     `json:"motd,omitempty"`
	IconURL         string                     `json:"icon_url,omitempty"`
	URLs            []string                   `json:"urls,omitempty"`
	Time            int64                      `json:"time,omitempty"`
	Nuts            map[string]json.RawMessage `json:"nuts"`
}

// SupportedNuts lists the NUT numbers the mint advertises as usable, sorted.
// NUT-04/05 count unless "disabled" is true; others need "supported": true
// (or a non-object value, which some mints use for NUT-15 and friends).
func (i *Info) SupportedNuts() []int {
	var out []int
	for k, v := range i.Nuts {
		n, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		var o map[string]json.RawMessage
		if json.Unmarshal(v, &o) != nil {
			out = append(out, n)
			continue
		}
		if d, ok := o["disabled"]; ok {
			if string(d) != "true" {
				out = append(out, n)
			}
			continue
		}
		if s, ok := o["supported"]; ok {
			if string(s) == "true" || strings.HasPrefix(string(s), "[") {
				out = append(out, n)
			}
			continue
		}
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// Info fetches GET /v1/info.
func (c *Client) Info(ctx context.Context, mint *url.URL) (*Info, Call, error) {
	var info Info
	call, err := c.do(ctx, http.MethodGet, mint, "/v1/info", nil, &info)
	if err != nil {
		return nil, call, err
	}
	return &info, call, nil
}

// Keyset is one entry of GET /v1/keysets (NUT-02).
type Keyset struct {
	ID          string `json:"id"`
	Unit        string `json:"unit"`
	Active      bool   `json:"active"`
	InputFeePPK uint64 `json:"input_fee_ppk"`
	FinalExpiry *int64 `json:"final_expiry,omitempty"`
}

// Keysets fetches GET /v1/keysets.
func (c *Client) Keysets(ctx context.Context, mint *url.URL) ([]Keyset, Call, error) {
	var resp struct {
		Keysets []Keyset `json:"keysets"`
	}
	call, err := c.do(ctx, http.MethodGet, mint, "/v1/keysets", nil, &resp)
	return resp.Keysets, call, err
}

// ProofState is one element of a NUT-07 checkstate answer.
type ProofState struct {
	Y       string  `json:"Y"`
	State   string  `json:"state"` // UNSPENT, PENDING or SPENT
	Witness *string `json:"witness,omitempty"`
}

// CheckState asks POST /v1/checkstate for the state of each Y, in batches,
// and returns the states in the order of ys.
func (c *Client) CheckState(ctx context.Context, mint *url.URL, ys []string) ([]ProofState, []Call, error) {
	var out []ProofState
	var calls []Call
	for start := 0; start < len(ys); start += CheckStateBatch {
		batch := ys[start:min(start+CheckStateBatch, len(ys))]
		var resp struct {
			States []ProofState `json:"states"`
		}
		call, err := c.do(ctx, http.MethodPost, mint, "/v1/checkstate", map[string]any{"Ys": batch}, &resp)
		calls = append(calls, call)
		if err != nil {
			return nil, calls, err
		}
		byY := make(map[string]ProofState, len(resp.States))
		for _, s := range resp.States {
			byY[strings.ToLower(s.Y)] = s
		}
		for _, y := range batch {
			s, ok := byY[strings.ToLower(y)]
			if !ok {
				return nil, calls, fmt.Errorf("mint did not return a state for Y %s", y)
			}
			out = append(out, s)
		}
	}
	return out, calls, nil
}
