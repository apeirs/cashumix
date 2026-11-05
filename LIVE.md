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

## Not tested live

cdk-cli, cashu.me, Minibits and other wallets; melt (paying a Lightning invoice); mints other
than testnut and Minibits.
