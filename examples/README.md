# Examples

- `live-nutshell.txt`: a full nutshell session through the gateway on the Nym mixnet. It
  mints 64 sat on testnut (fake Lightning), sends 21, has a second wallet receive them (swap),
  and shows `checkstate` going from UNSPENT to SPENT. The gateway log at the end lists every
  request, including the NUT-17 WebSocket.
- `live-cdk-cli.txt`: the same with cdk-cli 0.18.3, plus a melt to testnut's fake Lightning
  backend and a mint through a gateway without `--default`.
- `live-cli.txt`: `cashumix ip`, `info` and `keysets` over the mixnet, plus one `--direct`
  call for comparison.
- `live-test.txt`: output of `CASHUMIX_LIVE=1 go test -run Live -v .`.
- `proofs.json`: the NUT-00 V3 test-vector proofs as a plain proof list. Try
  `cashumix checkstate testnut.cashu.space examples/proofs.json`. The proofs were never issued
  by testnut, so they show as UNSPENT: a mint answers UNSPENT for any Y it has never seen.
