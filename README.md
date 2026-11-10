# cashumix

Cashu wallet traffic through the [Nym mixnet](https://nym.com), so the mint can't link your
mint, melt, swap and check-state requests to your IP address. Works with unmodified wallets:
tested with nutshell, cdk-cli and the cashu.me web wallet.

```
wallet ──http──> cashumix gateway ──SOCKS5──> nym-socks5-client ──Sphinx packets──> mixnet ──> exit gateway ──TLS──> mint
(nutshell,        127.0.0.1:3338                127.0.0.1:1080                                          (sees the exit's IP)
 cdk-cli,
 cashu.me)
```

## Why

Blind signatures stop a mint from linking the tokens it issued to the tokens it later redeems,
but the mint still sees an IP address for every wallet action. A mint that logs IPs can tie the
deposit (mint quote, Lightning invoice) to the withdrawal (melt) when both come from the same
address minutes apart, and can tie a sender to a receiver when both check the same token's
state, a risk NUT-07 itself points out. cashumix takes the IP out of that picture: the mint sees
one of the Nym exit gateways that all Nym users share, and a fixed `User-Agent`. On the way
there your requests travel as fixed-size Sphinx packets, mixed and delayed at every hop.
cashumix never touches the Cashu cryptography, and TLS runs from your machine to the mint, so
the exit relays only ciphertext.

## 60-second quickstart

You need Go and Nym's `nym-socks5-client` binary
([releases](https://github.com/nymtech/nym/releases), `nym-binaries-*`; check its sha256
against the release's `hashes.json`).

```bash
go install github.com/apeirs/cashumix/cmd/cashumix@latest
cashumix nym setup --binary ./nym-socks5-client   # picks a random Nym exit, starts the client on 127.0.0.1:1080 (under a minute)
cashumix ip                                       # should name a Nym exit gateway, not your address
cashumix serve --default testnut.cashu.space      # the gateway, on http://127.0.0.1:3338
```

Then, in any wallet, replace the mint URL `https://testnut.cashu.space` with
`http://127.0.0.1:3338/testnut.cashu.space`:

```bash
cdk-cli mint http://127.0.0.1:3338/testnut.cashu.space 64                   # cdk-cli
MINT_URL=http://127.0.0.1:3338/testnut.cashu.space cashu invoice 64          # nutshell
# cashu.me: Mints > Add mint > http://127.0.0.1:3338/testnut.cashu.space, allow "local network access"
```

testnut's Lightning is fake, so this costs nothing. For a real mint, use its host (and path)
the same way, e.g. `http://127.0.0.1:3338/mint.minibits.cash/Bitcoin`.

Without a wallet, the CLI queries a mint straight over the mixnet: `cashumix info
testnut.cashu.space` (NUT-06), `cashumix keysets <mint>` (NUT-02), and `cashumix checkstate
cashuB...` (NUT-07: is this token still unspent? It derives each proof's
`Y = hash_to_curve(secret)` locally and sends only the `Y`s). Written in Go; the only
dependency is `decred/dcrd/dcrec/secp256k1`.

## Tested wallets

Every row was run live over the Nym mixnet against testnut.cashu.space, with the gateway log
showing each request. Full sessions are in [examples/](examples/).

| Wallet | Version | What worked through cashumix | Setup that worked | Session |
|---|---|---|---|---|
| nutshell | 0.20.2 | mint, send, receive (swap), NUT-17 WebSocket | `cashumix serve --default testnut.cashu.space`, then with `MINT_URL=http://127.0.0.1:3338/testnut.cashu.space`: `cashu invoice 64`, `cashu send 21`, `cashu -w recipient receive <token>` | [live-nutshell.txt](examples/live-nutshell.txt) |
| cdk-cli | 0.18.3 | mint, send, receive, melt, NUT-17 WebSockets, a gateway without `--default` | `cashumix serve`, then `cdk-cli mint http://127.0.0.1:3338/testnut.cashu.space 64`, `cdk-cli send --mint-url <same> --amount 21`, `cdk-cli receive --allow-untrusted <token>`, `cdk-cli melt --mint-url <same> --invoice lnbc...` | [live-cdk-cli.txt](examples/live-cdk-cli.txt) |
| cashu.me | web, build 4abd2683 (2026-11-10), Chrome 152 | add mint, mint (incl. the batched `/v1/mint/bolt11/batch`), send, receive, melt, NUT-17 WebSockets, tokens passed both ways with cdk-cli | `cashumix serve --cors https://wallet.cashu.me`; Mints > Add mint > `http://127.0.0.1:3338/testnut.cashu.space`; allow Chrome's "local network access" prompt | [live-cashu-me.txt](examples/live-cashu-me.txt) |
| `cashumix` CLI | this repo | `info`, `keysets`, `checkstate` on testnut and mint.minibits.cash/Bitcoin | `cashumix info testnut.cashu.space` | [live-cli.txt](examples/live-cli.txt), [LIVE.md](LIVE.md) |

Not tested yet: mobile wallets (Minibits, eNuts), Firefox and Safari, nutshell's melt, and a
melt that pays a real Lightning invoice (the melts above went to testnut's fake backend).

## A real run

cashu.me mints and sends through the gateway, cdk-cli receives the token and sends some back,
cashu.me melts. Shortened from [examples/live-cashu-me.txt](examples/live-cashu-me.txt); the
exit's address is replaced with `<exit-ip>`.

```
$ cashumix ip
https://api.ipify.org sees this request coming from <exit-ip> (5.41s over the mixnet)
= Nym node 2943 (NL, exit gateway: true) in the nym-api node list
$ cashumix serve --cors https://wallet.cashu.me &

[cashu.me] Receive > Lightning > 32                    -> balance 71 sat after 11.2 s
[cashu.me] Send > Ecash > 8                            -> cashuBo2FteClodHRwOi8vMTI3LjAuMC4xOjMz...
$ cdk-cli -n -w x2 receive --allow-untrusted cashuBo2FteClodHRwOi8vMTI3LjAuMC4xOjMz...
Received: 7
$ cashumix checkstate token.txt
3 proofs at https://testnut.cashu.space: 8 sat SPENT (checked over the mixnet in 3.90s)
[cashu.me] Receive > Ecash > (5 sat from cdk-cli)      -> 67 available after 2.2 s
[cashu.me] Send > Lightning > lnbc50n1... > Pay        -> "Paid 5, Fee: 1" after 7.1 s

gateway log:
09:53:59 POST testnut.cashu.space/v1/mint/quote/bolt11 -> 200, 527 bytes, 6.19s
09:54:03 POST testnut.cashu.space/v1/mint/bolt11/batch -> 200, 1577 bytes, 1.03s
09:56:11 GET testnut.cashu.space/v1/ws -> 101, 0 bytes, 131.90s (websocket)
09:56:31 POST testnut.cashu.space/v1/swap -> 200, 1576 bytes, 1.38s
09:57:04 POST testnut.cashu.space/v1/melt/quote/bolt11 -> 200, 404 bytes, 1.96s
09:57:39 POST testnut.cashu.space/v1/melt/bolt11 -> 200, 424 bytes, 6.26s
```

## Limits, in short

- **It hides who connects, not what you send.** The mint still sees amounts, quote ids,
  proofs and invoices, and can link requests by content and timing. Details below.
- **Lightning is out of scope.** The node that pays your mint invoice, or that you melt to,
  is not protected.
- **Only the mint traffic goes through the mixnet.** Your wallet's other traffic doesn't:
  cashu.me's onboarding fetches `/v1/info` directly from every mint it recommends (16 on
  the day of the test, testnut among them) before you add the gateway URL, and its Nostr features (NIP-17 messages, mint backups) talk to
  relays directly.
- **Tokens you send name the gateway URL** (`http://127.0.0.1:3338/...`). Run
  `cashumix unwrap` on them before giving them to someone without cashumix.
- **It is slow.** About 1 to 10 seconds per request, sometimes much more.
- **https mints on port 443 only by default**, no `.onion` mints, and the mint learns that
  you use Nym.

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

**cdk-cli** (0.18.3 tested: mint, send, receive and melt, see
[examples/live-cdk-cli.txt](examples/live-cdk-cli.txt)): pass the gateway URL wherever cdk-cli
takes a mint URL. cdk-cli keeps the mint path in its WebSocket URL, so it doesn't need
`--default`.

```bash
cashumix serve &
M=http://127.0.0.1:3338/testnut.cashu.space
cdk-cli mint $M 64
cdk-cli send --mint-url $M --amount 21
cdk-cli melt --mint-url $M --invoice lnbc...
```

**cashu.me** (tested on wallet.cashu.me in Chrome 152: mint, send, receive and melt, see
[examples/live-cashu-me.txt](examples/live-cashu-me.txt)):

```bash
cashumix serve --cors https://wallet.cashu.me &
```

1. In cashu.me, add the mint `http://127.0.0.1:3338/testnut.cashu.space` (onboarding's "Add
   mint" field, or Mints > Add mint).
2. Chrome (142 and later) asks whether wallet.cashu.me may access your local network or this
   device. Allow it. If you refuse or miss the prompt, Chrome blocks the request before it
   reaches the gateway and cashu.me says "This mint is not reachable"; re-allow it in the
   site settings ("Local network access").
3. Use the wallet as usual. cashu.me keeps the mint path in its NUT-17 WebSocket URL, so it
   needs no `--default`.

`--cors https://wallet.cashu.me` matters for browser wallets. Any web page you visit can send
requests to `127.0.0.1`. With the default `--cors '*'`, a page that gets past the browser's
local-network check could use your gateway, and through it your mixnet client. With a list of
origins, the gateway refuses every request whose `Origin` header is not on it, WebSocket
upgrades included (browsers don't apply CORS to WebSockets). CLI wallets send no `Origin` and
are not affected. The status page `/.cashumix`, which lists the mints you used, is never
available to web pages.

Other browser wallets should work the same way: add the mint as `http://127.0.0.1:3338/<mint>`
and put the wallet's origin in `--cors`. Browsers treat `127.0.0.1` as a secure context, so an
https wallet page may call it. The gateway answers CORS preflights itself (they never cross the
mixnet), including Chrome's older Private Network Access header.

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
| `--cors` | `*` | web wallet origins allowed, comma-separated (e.g. `https://wallet.cashu.me`); others get 403; `*` any page; `-` no CORS headers |
| `--host` | loopback only | extra accepted Host names (DNS-rebinding guard) |

`GET /.cashumix` shows request counts per mint (for you and local tools; web pages get 403).

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
- **Web pages.** With `--cors <origins>`, a request with any other `Origin` is refused,
  WebSockets included. `/.cashumix` and the help page refuse any request that has an `Origin`.
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
  invoice, a wallet that talks to some mints directly (cashu.me's mint discovery fetches
  `/v1/info` from recommended mints without the gateway), Nostr features that reach relays
  directly, or NUT-21/22 clear auth (a JWT that names you by design).
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
- the `--cors` origin allowlist (HTTP, preflight and WebSocket) and a status page web pages
  can't read;
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
