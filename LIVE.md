# Live run on the Nym mixnet

Recorded 2026-11-05 with nym-socks5-client (`nym-binaries` release built 2026-10-01). The
client was set up with `cashumix nym setup`, which picked exit gateway node 2943 (NL) at
random from 99 active exits with an exit-policy network requester. Reply SURBs were on.
Everything below is real output; nothing was edited except where noted.

## `go test -run Live` (second run)

```
LIVE exit-ip: servers see 152.53.105.12 (4.24s over the mixnet)
LIVE exit-ip: a direct request comes from this machine's own address (not shown); same as the mixnet one: false
LIVE exit-ip: 152.53.105.12 is Nym node 2943 (NL), exit gateway: true
LIVE https://testnut.cashu.space /v1/info over the mixnet: "Testnut mint" cdk-mintd/0.18.1, nuts [4 5 7 8 9 10 11 12 14 15 17 19 20 29], 4.69s
LIVE https://testnut.cashu.space /v1/keysets over the mixnet: 2 keysets, 0.85s
LIVE https://testnut.cashu.space /v1/checkstate over the mixnet: random Y 0202af374feefdef... is UNSPENT, 0.72s
LIVE https://mint.minibits.cash/Bitcoin /v1/info over the mixnet: "Minibits mint" cdk-mintd/0.17.7, nuts [4 5 7 8 9 10 11 12 14 15 17 19 20 29], 3.73s
LIVE https://mint.minibits.cash/Bitcoin /v1/keysets over the mixnet: 3 keysets, 0.93s
LIVE https://mint.minibits.cash/Bitcoin /v1/checkstate over the mixnet: random Y 02a5025ae14fddd7... is UNSPENT, 1.03s
LIVE gateway http://127.0.0.1:34737/testnut.cashu.space/v1/info -> "Testnut mint", 2.66s (fresh mixnet stream)
LIVE gateway http://127.0.0.1:34737/testnut.cashu.space/v1/checkstate -> UNSPENT, 2.35s
LIVE gateway http://127.0.0.1:34737/mint.minibits.cash/Bitcoin/v1/info -> "Minibits mint", 2.04s (fresh mixnet stream)
LIVE gateway http://127.0.0.1:34737/mint.minibits.cash/Bitcoin/v1/checkstate -> UNSPENT, 2.73s
--- PASS: TestLive (26.89s)
```

The first request to each mint includes a DNS-over-HTTPS lookup over the mixnet and a TLS
handshake. Requests on an open connection take about 1 second; a fresh stream per request
(`--fresh`, used by the gateway part of the test) takes about 2 to 3 seconds.

The same test also passed in an earlier run that took 125.8 s. Most requests took 1 to 10 s,
but one, `GET mint.minibits.cash/Bitcoin/v1/keysets`, took 70.6 s. Mixnet latency varies,
which is why the timeouts are generous (3 min for the CLI, 5 min per gateway request).

For comparison, `cashumix info testnut.cashu.space --direct` took 0.26 s.

## A real wallet: nutshell through the gateway

`nutshell` 0.20.2 with `MINT_URL=http://127.0.0.1:3338/testnut.cashu.space` and
`cashumix serve --default testnut.cashu.space`. It minted 64 sat (testnut's Lightning is fake),
sent a 21 sat token, and a second wallet received it (a swap). `cashumix checkstate` showed the
token's three proofs go from UNSPENT to SPENT. Every request went over the mixnet, the NUT-17
WebSocket included:

```
13:46:00 POST testnut.cashu.space/v1/mint/quote/bolt11 -> 200, 527 bytes, 1.02s
13:46:06 POST testnut.cashu.space/v1/mint/bolt11 -> 200, 4697 bytes, 2.67s
13:46:07 GET testnut.cashu.space/v1/ws -> 101, 0 bytes, 6.82s (websocket)
13:48:21 POST testnut.cashu.space/v1/swap -> 200, 3136 bytes, 1.19s
```

The full session is in [examples/live-nutshell.txt](examples/live-nutshell.txt). The
UNSPENT `checkstate` table in that file was copied from the terminal when it ran; every other
block was captured directly to the file.

First attempt, without `--default`: nutshell opened its WebSocket at `ws://127.0.0.1:3338/v1/ws`
and dropped the mint path. The gateway refused that socket, because it names no mint, and
`cashu invoice` waited forever. That led to the `--default` advice in the README and to
logging refused requests in the gateway.

## A second wallet: cdk-cli

Recorded 2026-11-07, same client identity and exit (node 2943, NL). `cdk-cli` 0.18.3, the
prebuilt release binary (sha256 checked against the release's `SHA256SUMS`), with the mint URL
`http://127.0.0.1:3338/testnut.cashu.space` and `cashumix serve --default testnut.cashu.space`.
Wallet w1 minted 64 sat (13.6 s, including testnut's fake payment), sent a 21 sat token
(offline, no request), and wallet w2, in its own `--work-dir`, received it (a swap, 4.2 s;
20 sat after the input fee). `cashumix checkstate` showed the token's three proofs go from
UNSPENT to SPENT. Then w1 melted 10 sat (6.9 s); its balance went from 43 to 30 sat. Every
request went over the mixnet, both NUT-17 WebSockets included:

```
00:06:36 POST testnut.cashu.space/v1/mint/quote/bolt11 -> 200, 527 bytes, 0.81s
00:06:42 POST testnut.cashu.space/v1/mint/bolt11 -> 200, 4697 bytes, 2.35s
00:06:43 GET testnut.cashu.space/v1/ws -> 101, 0 bytes, 7.31s (websocket)
00:07:22 POST testnut.cashu.space/v1/swap -> 200, 3136 bytes, 0.90s
00:08:07 POST testnut.cashu.space/v1/melt/quote/bolt11 -> 200, 487 bytes, 0.93s
00:08:08 POST testnut.cashu.space/v1/swap -> 200, 640 bytes, 0.81s
00:08:09 POST testnut.cashu.space/v1/melt/bolt11 -> 200, 488 bytes, 0.81s
00:08:13 GET testnut.cashu.space/v1/melt/quote/bolt11/01a1b31e-905e-7613-b096-69a649b18bcb -> 200, 507 bytes, 1.11s
00:08:17 GET testnut.cashu.space/v1/ws -> 101, 0 bytes, 8.46s (websocket)
```

The full session is in [examples/live-cdk-cli.txt](examples/live-cdk-cli.txt). No gateway
change was needed.

- **The melt paid nothing real.** testnut's fake backend marks any well-formed bolt11 invoice
  paid. The invoice was made locally with a random payee key and a random payment hash. The
  first one had no feature bits, and cdk-cli refused it locally ("The invoice's features are
  invalid") before asking for a melt quote. With `var_onion_optin` and `payment_secret`
  required, it went through.
- **cdk-cli keeps the mint path in its WebSocket URL.** A second gateway without `--default`
  (port 3339, a third wallet) minted 8 sat, and its log shows
  `GET testnut.cashu.space/v1/ws -> 101`. Unlike nutshell, cdk-cli needs no `--default`.
- **`send` with `-n`** (non-interactive) needs `--mint-url`: the wallet lists testnut twice,
  as sat and usd, and otherwise asks which one to send from.

## A web wallet: cashu.me

Recorded 2026-11-10, same client identity and exit (node 2943, NL). wallet.cashu.me (build
4abd2683) in Chrome 152, driven by Playwright, a fresh wallet, with the mint
`http://127.0.0.1:3338/testnut.cashu.space`. cdk-cli 0.18.3 was the other wallet.

First run (gateway with the default `--cors '*'`):

- **Adding the mint failed at first, before any request left the browser.** Chrome 152 blocks
  an https page's requests to `127.0.0.1` until the user allows "local network access":
  "Permission was denied for this request to access the `loopback` address space", and cashu.me
  showed "This mint is not reachable". A normal Chrome shows a prompt; the headless one denied
  it silently. After the permission was granted through Playwright, the same "Add mint" worked
  (`GET testnut.cashu.space/v1/info -> 200, 0.95s`). No gateway change could have helped:
  the block happens in the browser. The README now says to allow the prompt.
- Then: mint 64 sat; send 21 (cashu.me swapped first, `POST /v1/swap -> 200`); cdk-cli
  received the token in 7.3 s (`Received: 20`, after testnut's input fee); `cashumix
  checkstate` showed its three proofs UNSPENT before and SPENT after; cdk-cli sent 10 back and
  cashu.me received them.
- **A melt to an invoice from testnut itself failed, correctly.** The invoice came from a
  testnut mint quote, which testnut's fake backend marks paid at once, so the melt got
  HTTP 400, "Invoice already paid.". The gateway passed the error through and cashu.me
  showed it. A melt to a locally made invoice (random payee key and payment hash, with
  `var_onion_optin` and `payment_secret` required, as in the cdk-cli run) then went through:
  "Paid 10, Fee: 1".

Second run, after the `--cors` change below, with `cashumix serve --cors
https://wallet.cashu.me`: mint 32 (11.2 s from "Create invoice" to the new balance), send 8,
cdk-cli receive (6.9 s), cdk-cli sends 5 back, cashu.me receives (2.2 s), melt 5 (7.1 s). The
full session and the gateway log are in
[examples/live-cashu-me.txt](examples/live-cashu-me.txt). Every request in the gateway log
returned 200 or 101, including cashu.me's batched `POST /v1/mint/bolt11/batch`, its
`POST /v1/mint/quote/bolt11/check`, its checkstate polling of a pending token, and its NUT-17
WebSockets, which keep the mint path in their URL (no `--default` needed).

What the browser's network log showed besides the gateway: during onboarding, cashu.me's mint
discovery fetched `/v1/info` straight from the mints it recommends (testnut and Minibits
among them), and the wallet connects to Nostr relays directly. None of that carries wallet
operations, but those mints and relays see your IP once. After the gateway URL was added,
every mint request in the session went to `127.0.0.1:3338`.

**A bug this found in cashumix:** the status page `/.cashumix` lists the mints the gateway
has talked to, and it was served with `Access-Control-Allow-Origin: *`. Any web page that got
past the browser's local-network check could read which mints you use. Now the status and help
pages refuse any request that has an `Origin` header and carry no CORS headers, and `--cors`
takes a list of wallet origins: requests from other origins get 403, WebSocket upgrades
included. Tests: `TestStatusPagePrivateAndOriginAllowlist`.

## Not tested live

Minibits, eNuts and other mobile wallets; Firefox and Safari; a melt that pays a real
Lightning invoice (every melt above went to testnut's fake backend); nutshell's melt; mints
other than testnut and Minibits.
