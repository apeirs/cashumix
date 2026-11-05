// Package cashumix puts Cashu wallet <-> mint traffic on the Nym mixnet.
//
// See the README and cmd/cashumix. The packages are:
//
//	mixnet   SOCKS5 to nym-socks5-client, DoH over the mixnet, no clearnet fallback
//	nym      sets up and runs the official nym-socks5-client
//	cashu    NUT-00 hash_to_curve, cashuA/cashuB tokens, NUT-06/02/07 client
//	gateway  local mint endpoint http://127.0.0.1:3338/<mint> for unmodified wallets
package cashumix
