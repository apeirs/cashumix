# cashumix

Cashu wallet traffic through the [Nym mixnet](https://nym.com), so the mint can't link your
mint, melt, swap and check-state requests to your IP address.

```
wallet ──http──> cashumix gateway ──SOCKS5──> nym-socks5-client ──Sphinx packets──> mixnet ──> exit gateway ──TLS──> mint
(nutshell,        127.0.0.1:3338                127.0.0.1:1080                                          (sees the exit's IP)
 cdk-cli,
 cashu.me)
```

Ecash hides *what* you spend: blind signatures stop the mint linking the tokens it issued to the
tokens it later redeems. It does not hide *who is talking*. Every request reaches the mint over
HTTP from your IP, with your wallet's headers and timing. A mint that logs IPs can still tie the
deposit (mint quote, Lightning invoice) to the withdrawal (melt) when both come from the same
address minutes apart. It can also tie a sender to a receiver when both check the same token's
state, a risk NUT-07 itself points out. Anyone watching your connection can do the same.

cashumix removes the IP from that picture. The mint sees a Nym exit gateway's address, which
many Nym users share, and a fixed `User-Agent`. On the way to the exit your requests travel
as fixed-size Sphinx packets, mixed and delayed at every hop. cashumix never touches the Cashu
cryptography: blinding, signatures and proofs stay between your wallet and the mint, and TLS
runs from your machine to the mint, so the exit relays only ciphertext.

It has two parts:

- **`cashumix serve`**: a local gateway that any Cashu wallet can use. You don't patch the
  wallet. You change one setting, the mint URL:
  `https://mint.example.com` becomes `http://127.0.0.1:3338/mint.example.com`.
- **`cashumix info | keysets | checkstate`**: read-only mint queries straight over the mixnet,
  including a NUT-07 token-state check that derives each proof's `Y = hash_to_curve(secret)`
  locally (NUT-00, tested against the spec's vectors).

Written in Go. The only dependency is `decred/dcrd/dcrec/secp256k1`.

## Quick start

```bash
go install github.com/apeirs/cashumix/cmd/cashumix@latest   # or: go build ./cmd/cashumix

# 1. The mixnet client (Nym's official binary, see below)
cashumix nym setup --binary ./nym-socks5-client     # picks a random active exit gateway, starts it on 127.0.0.1:1080

# 2. Check it works: the IP that servers see should be a Nym exit gateway
cashumix ip

# 3. Talk to a mint over the mixnet
cashumix info testnut.cashu.space
cashumix keysets mint.minibits.cash/Bitcoin
cashumix checkstate cashuB...                      # is this token still unspent?

# 4. Run the gateway for your wallet
cashumix serve
```

Real output from these commands is in [LIVE.md](LIVE.md) and [examples/](examples/).

## Running nym-socks5-client

cashumix does not reimplement the mixnet. Nym's own `nym-socks5-client` does the Sphinx
packets, cover traffic, Poisson delays and gateway handling. It exposes a local SOCKS5 port, and
cashumix speaks SOCKS5 to that port.

1. Download `nym-socks5-client` for your platform from the
   [Nym releases](https://github.com/nymtech/nym/releases) (`nym-binaries-*`). Check its sha256
   against the release's `hashes.json`, then `chmod +x` it.
2. `cashumix nym setup --binary ./nym-socks5-client` (or set `CASHUMIX_NYM_BINARY`). This asks
   the public nym-api for active exit gateways that run a network requester with the exit
   policy, picks one at random, runs `nym-socks5-client init` with anonymous replies (reply
   SURBs: the exit never learns your mixnet address), and starts the client. Startup usually
   takes under a minute.
3. Later: `cashumix nym start | stop | status | exits`. The config lives in
   `~/.nym/socks5-clients/cashumix/`; logs and the pid file in `$XDG_STATE_HOME/cashumix/`.

To do it by hand:

```bash
nym-socks5-client init --id cashumix --provider <network-requester-address> --port 1080 --use-reply-surbs true
nym-socks5-client run --id cashumix
```

cashumix finds the client at `127.0.0.1:1080`. Point it elsewhere with
`--proxy host:port` or `CASHUMIX_NYM_SOCKS5=host:port`. There is **no clearnet fallback**. If
the SOCKS5 port is not listening, every request fails with an error. Only `--direct` connects
without the mixnet, and it exists only for comparison and testing.

## Pointing a wallet at the gateway

Start the gateway:

```bash
cashumix serve                      # http://127.0.0.1:3338
cashumix serve --default testnut.cashu.space   # also route host-less paths (see nutshell)
```

Then replace the mint URL in the wallet:

| Mint | Mint URL to give the wallet |
|---|---|
| `https://testnut.cashu.space` | `http://127.0.0.1:3338/testnut.cashu.space` |
| `https://mint.minibits.cash/Bitcoin` | `http://127.0.0.1:3338/mint.minibits.cash/Bitcoin` |
| `https://mint.example.com:3338` | `http://127.0.0.1:3338/mint.example.com:3338` (needs `--allow-port 3338`) |

The gateway forwards `http://127.0.0.1:3338/<host>/<path>` to `https://<host>/<path>`. It keeps
the path and query and always uses https upstream.

**nutshell** (tested, see [examples/live-nutshell.txt](examples/live-nutshell.txt)):

```bash
cashumix serve --default testnut.cashu.space &
MINT_URL=http://127.0.0.1:3338/testnut.cashu.space cashu info
MINT_URL=http://127.0.0.1:3338/testnut.cashu.space cashu invoice 64
MINT_URL=http://127.0.0.1:3338/testnut.cashu.space cashu send 21
```

nutshell (0.20.2) opens its NUT-17 WebSocket at `ws://<host:port>/v1/ws` and drops the mint
URL's path. Without `--default`, the gateway can't tell which mint that socket is for and
refuses it, and `cashu invoice` then waits forever for the "paid" notification. With
`--default <mint>`, host-less paths such as `/v1/ws` go to that mint, and everything works,
WebSocket included. The same nutshell behaviour breaks any mint whose URL has a path, such as
`mint.minibits.cash/Bitcoin`, even without cashumix.

**cdk-cli** (not tested here): pass the gateway URL wherever cdk-cli takes a mint URL. For
example, `cdk-cli mint http://127.0.0.1:3338/testnut.cashu.space 64`.

**cashu.me and other browser wallets** (not tested here): add the mint as
`http://127.0.0.1:3338/<mint>`. Browsers treat `127.0.0.1` as a secure context, so an https
wallet page may call it. The gateway answers CORS preflights itself (they never cross the
mixnet), including Chrome's Private Network Access header. `--cors` restricts the allowed
origin.

**Mobile wallets** (Minibits, eNuts): only if the gateway runs on the phone itself or on a
network you control (`--listen 0.0.0.0:3338 --public --host <name>`). Anyone who can reach a
public gateway can use your mixnet client.

### Tokens you send carry the gateway URL

A wallet that uses the gateway believes its mint is `http://127.0.0.1:3338/testnut.cashu.space`
and writes that URL into the tokens it creates. A recipient without cashumix can't reach it.
Before sending, fix the token:

```bash
cashumix unwrap cashuB...      # prints the same token with mint https://testnut.cashu.space
```

`checkstate` and `decode` understand such URLs anyway. A recipient who runs cashumix can
receive the token as it is.

## Commands

```
cashumix nym setup | start | stop | status | exits     the mixnet client
cashumix serve [flags]                                 the gateway
cashumix info MINT                                     NUT-06 mint info, supported NUTs, latency
cashumix keysets MINT                                  NUT-02 keysets: id, unit, active, input fee
cashumix checkstate [MINT] INPUT                       NUT-07: UNSPENT / PENDING / SPENT per proof
cashumix decode INPUT                                  offline: mint, amounts, keyset ids, Ys
cashumix unwrap INPUT                                  offline: put the real mint URL back into a token
cashumix ip                                            which IP servers see (and whether it is a Nym node)
```

`INPUT` is a `cashuA…` (V3) or `cashuB…` (V4) token, a `cashu:` URI, a file containing one, a
raw `crawB` binary token, a JSON proof array, `{"proofs": [...]}`, or `-` for stdin. Without a
`MINT` argument, `checkstate` uses the mint named in the token. `checkstate` sends the mint only
the `Y` values, never secrets or signatures. `decode` hides secrets unless you pass `--secrets`.

Common flags: `--proxy` (`$CASHUMIX_NYM_SOCKS5`, default `127.0.0.1:1080`), `--dns doh|exit`,
`--direct`, `-v` (trace every mixnet step), `--json`, `--timeout`.

Gateway flags:

| Flag | Default | What it does |
|---|---|---|
| `--listen` | `127.0.0.1:3338` | local address; non-loopback needs `--public` |
| `--default` | none | mint for paths without a host (`/v1/...`) |
| `--only` | any | comma-separated allowlist of mint hosts |
| `--allow-port` | `443` | upstream ports allowed |
| `--fresh` | off | a new mixnet stream and TLS session for every request |
| `--user-agent` | `cashumix` | the one User-Agent every mint sees |
| `--max-body` / `--max-response` | 1 MiB / 16 MiB | body size limits |
| `--request-timeout` | 5m | per request (WebSockets exempt) |
| `--cors` | `*` | `Access-Control-Allow-Origin`; `-` for none |
| `--host` | loopback only | extra accepted Host names (DNS-rebinding guard) |

`GET /.cashumix` shows request counts per mint.

## What the gateway changes and checks

- **Headers.** Toward the mint, only `Accept`, `Content-Type` and the NUT-21/22 `Clear-auth` /
  `Blind-auth` headers are forwarded, plus the WebSocket handshake headers on an upgrade.
  `User-Agent` is replaced. Cookies, `Origin`, `Referer`, `Accept-Language`, client hints,
  `Authorization`, `X-Forwarded-For`, `X-Real-IP`, `Forwarded` and `Via` are dropped. On the
  way back, `Set-Cookie`, `Server`, `Alt-Svc` and other non-essential headers are dropped.
- **Names.** By default (`--dns doh`), mint hostnames are resolved with DNS-over-HTTPS that
  itself goes through the mixnet (Cloudflare, then Google, reached by IP). The exit is then
  asked for the IPv4 address, because some exits have broken IPv6. With `--dns exit`, the exit
  resolves the name. Your local resolver is never used.
- **Upstream.** https only, with certificate verification. No private, loopback or link-local IP
  literals. `.onion` hosts are refused (Nym exits can't reach them). Optional host allowlist
  and port list.
- **Limits.** Request and response size caps, generous timeouts (a mixnet round trip takes
  seconds), and a Host-header check so a web page can't reach the gateway through DNS rebinding.
- **WebSockets.** NUT-17 subscriptions (`/v1/ws`) pass through.
- **Connections.** By default the gateway keeps one connection per mint open for a short
  while, because each new one costs several mixnet round trips. With `--fresh`, every request
  gets its own SOCKS5 stream and TLS session. The mint then can't group requests by connection,
  only by timing.

## Threat model and limits

The mixnet hides **who** connects: your IP, and your timing as seen on the network. It does not
hide **what** you send. The mint still sees every request in full: amounts, keyset ids,
quote ids, blinded messages, proofs, and Lightning invoices it pays or issues. Ecash privacy
against the mint comes from blind signatures, not from cashumix. cashumix only closes the
network side channel.

- **The mint can still link requests by content.** A mint quote and its `/v1/mint` call share
  a quote id. A melt reveals the invoice you pay. A swap links the proofs it spends to the
  outputs it signs. A checkstate of a token you sent links you to it by `Y`.
- **The mint can still link requests by timing**, and does so better when they are few and
  close together. `--fresh` removes connection reuse but not timing. Spacing operations out
  helps.
- **Exit gateways are shared but few.** The mint sees one of a few hundred Nym exit IPs. That
  hides you among Nym users, but it shows the mint that you use Nym. Some mints or CDNs may
  rate-limit or block exit IPs.
- **The exit sees the mint's hostname** (in the SOCKS request and TLS SNI) and the traffic
  volume, but not the content. TLS runs end to end to the mint.
- **Your wallet itself** may still leak. Examples: the Lightning node that pays your mint
  invoice, a wallet that talks to some mints directly, or NUT-21/22 clear auth (a JWT that names
  you by design).
- **Lightning is out of scope.** cashumix protects the wallet-to-mint HTTP traffic only.
- **Speed.** Expect roughly 1 to 10 seconds per request, sometimes more. LIVE.md has measured
  numbers.

**Compared with Tor:** Tor gives lower latency, `.onion` mints, and a much larger user base,
but its circuits carry traffic without cover traffic or mixing, so an observer who sees both
ends can correlate timing. Nym adds per-hop delays and cover traffic against exactly that
observer, at the cost of latency.

## Tests

```bash
go vet ./... && go test ./...
```

The offline suite runs without a network. It uses a fake `nym-socks5-client` that records every
SOCKS5 CONNECT, a fake DoH resolver and a fake TLS mint. It covers:

- `hash_to_curve` against the three NUT-00 test vectors, plus the two blinded-message vectors
  (`B_ = Y + rG`);
- cashuA tokens: valid, padded, unpadded, bad prefix, no prefix;
- cashuB tokens: single and multiple keysets, the raw `crawB` form, and byte-exact
  re-encoding;
- a CBOR decoder with RFC 8949 examples and malformed input;
- URL mapping and allow/deny rules;
- header stripping in both directions and CORS preflight;
- the DNS-rebinding guard and body limits;
- TLS verification behind the exit;
- connection reuse vs `--fresh`;
- no clearnet fallback when the proxy is down;
- DoH over the mixnet;
- WebSocket passthrough and timeouts.

The live test talks to public mints through a real `nym-socks5-client`:

```bash
CASHUMIX_LIVE=1 go test -run Live -v -timeout 20m .
# CASHUMIX_NYM_SOCKS5=127.0.0.1:1080  CASHUMIX_LIVE_MINTS=testnut.cashu.space,mint.minibits.cash/Bitcoin
```

## Layout

```
cmd/cashumix   the CLI
gateway        the local mint gateway
cashu          hash_to_curve, cashuA/cashuB/crawB tokens, NUT-06/02/07 client
mixnet         SOCKS5 to nym-socks5-client, DoH over the mixnet, no fallback
nym            runs the official nym-socks5-client
internal/fakes offline SOCKS5 proxy, DoH resolver and mint for tests
```

The `mixnet` and `nym` packages come from apeir's
[nostrmix](https://github.com/apeirs/nostrmix) and [zecmix](https://github.com/apeirs/zecmix),
which do the same for Nostr relays and Zcash lightwalletd.

## License

MIT, see [LICENSE](LICENSE).
