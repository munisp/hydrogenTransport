package mojaloop

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"time"
)

// Minimal ILPv4 (Interledger Protocol) support, matching the wire format the
// Mojaloop FSPIOP API carries in the `ilpPacket` / `condition` / `fulfilment`
// fields of quotes and transfers.
//
// In a full Mojaloop deployment the PAYEE DFSP generates the ilpPacket and
// condition in the quote response and the payer forwards them verbatim into
// POST /transfers. H2Fleet additionally supports generating them locally
// (Config.GenerateILP, and as a fallback when the peer did not include them in
// the quote) so the flow works end-to-end against the mojaloop/simulator and
// the sdk-scheme-adapter test harness. The fulfilment is derived
// deterministically from the transfer id + a per-deployment secret so retries
// reproduce the exact same packet and the fulfilment can be re-verified
// offline: condition == SHA256(fulfilment).

// ilpTypePrepare is the ILPv4 "Prepare" packet type code.
const ilpTypePrepare = 12

// ilpTimestampLayout is the OER timestamp format used inside ILP packets:
// YYYYMMDDHHMMSS.mmmZ (19 bytes, ASCII).
const ilpTimestampLayout = "20060102150405.000Z"

// ilpDestination is the ILP address H2Fleet routes fare payments under.
// In a real switch the address is assigned by the scheme; the simulator
// accepts any well-formed address.
const ilpDestination = "g.moja.h2fleet"

// deriveFulfilment deterministically derives the 32-byte fulfilment preimage
// for a transfer. Deterministic derivation is what makes transfer retries
// safe: replaying the same transfer id always yields the same packet,
// condition and fulfilment.
func deriveFulfilment(transferID, secret string) [32]byte {
	return sha256.Sum256([]byte("h2fleet:ilp-fulfilment:v1:" + secret + ":" + transferID))
}

// conditionFor returns the ILP condition for a fulfilment: SHA-256 of the
// preimage, base64url-encoded (no padding) per the FSPIOP API.
func conditionFor(fulfilment [32]byte) (conditionB64 string, conditionRaw [32]byte) {
	conditionRaw = sha256.Sum256(fulfilment[:])
	return base64.RawURLEncoding.EncodeToString(conditionRaw[:]), conditionRaw
}

// encodeILPPrepare serializes an ILPv4 Prepare packet:
//
//	byte        0    : type (12 = Prepare)
//	byte        1    : destination address length
//	bytes       2..  : destination address (ILP address string)
//	8 bytes          : amount (uint64 big-endian)
//	19 bytes         : expiresAt (YYYYMMDDHHMMSS.mmmZ)
//	4 bytes          : condition length (always 32)
//	32 bytes         : condition (SHA-256 of the fulfilment)
//	4 bytes          : data length
//	bytes            : data (optional, unused → 0)
func encodeILPPrepare(destination string, amountMinor uint64, expiresAt time.Time, condition [32]byte) []byte {
	expiry := expiresAt.UTC().Format(ilpTimestampLayout)
	pkt := make([]byte, 0, 2+len(destination)+8+len(expiry)+4+32+4)
	pkt = append(pkt, ilpTypePrepare)
	pkt = append(pkt, byte(len(destination)))
	pkt = append(pkt, destination...)
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], amountMinor)
	pkt = append(pkt, tmp[:]...)
	pkt = append(pkt, expiry...)
	binary.BigEndian.PutUint32(tmp[:4], uint32(len(condition)))
	pkt = append(pkt, tmp[:4]...)
	pkt = append(pkt, condition[:]...)
	binary.BigEndian.PutUint32(tmp[:4], 0) // data length
	pkt = append(pkt, tmp[:4]...)
	return pkt
}

// buildILP returns the FSPIOP-ready (ilpPacket, condition, fulfilment) triple
// for a transfer, all base64url-encoded as the FSPIOP API expects. The
// fulfilment MUST be retained by the caller: it is what the payee returns in
// the transfer response to prove settlement, and it can be re-derived from
// (transferID, secret) for verification.
func buildILP(transferID, secret string, amountMinor uint64, expiresAt time.Time) (ilpPacket, condition, fulfilment string) {
	f := deriveFulfilment(transferID, secret)
	condB64, condRaw := conditionFor(f)
	pkt := encodeILPPrepare(ilpDestination, amountMinor, expiresAt, condRaw)
	return base64.RawURLEncoding.EncodeToString(pkt), condB64, base64.RawURLEncoding.EncodeToString(f[:])
}

// verifyFulfilment checks that a fulfilment returned by the payee satisfies a
// condition: SHA256(fulfilment) must equal the condition bytes.
func verifyFulfilment(fulfilmentB64, conditionB64 string) bool {
	f, err := base64.RawURLEncoding.DecodeString(fulfilmentB64)
	if err != nil {
		return false
	}
	c, err := base64.RawURLEncoding.DecodeString(conditionB64)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(f)
	return string(sum[:]) == string(c)
}
