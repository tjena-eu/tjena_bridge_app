package main

// Moving without a camera: the two devices agree on the transfer key with
// ECDH (P-256) through the gateway's pipe, and the user compares a 6-digit
// code on both screens. The new device commits to its public key before it
// sees the old one's, so a server in the middle gets one guess (1 in a
// million) per attempt and can't search for matching codes.
//
//	new → old  {"type":"hello","mode":"compare","commit":b64(SHA256(pubNew))}
//	old → new  {"type":"key","pub":b64(pubOld)}
//	new → old  {"type":"key","pub":b64(pubNew)}       old checks the commit
//	both       show the code; new → old {"type":"confirm"} when the user taps
//	old        sends once both sides confirmed
//
// shared = ECDH x-coordinate, salt = SHA256(pubOld || pubNew),
// key = HKDF-SHA256(shared, salt, "tjena-bridge move key", 32),
// code = HKDF(…, "tjena-bridge move sas", 4) as big-endian uint32 mod 10^6.
// Public keys are uncompressed points, base64url without padding.
//
// With the QR code: new → old {"type":"hello","mode":"code","proof":…}.
// proof and the "imported" proof are HMAC-SHA256(key, label), base64url.
// Same in bridge_app (MoveKex.kt).

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	proofHello    = "tjena-bridge move hello"
	proofImported = "tjena-bridge move imported"
)

var b64 = base64.RawURLEncoding

func moveProof(key []byte, label string) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(label))
	return b64.EncodeToString(h.Sum(nil))
}

func checkProof(key []byte, label, got string) bool {
	return hmac.Equal([]byte(moveProof(key, label)), []byte(got))
}

type kex struct {
	priv *ecdh.PrivateKey
	pub  []byte
}

func newKex() (*kex, error) {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &kex{k, k.PublicKey().Bytes()}, nil
}

func (k *kex) pubB64() string { return b64.EncodeToString(k.pub) }

func commitOf(pub []byte) string {
	h := sha256.Sum256(pub)
	return b64.EncodeToString(h[:])
}

// derive: key and display code from our key and the peer's public key.
// oldPub/newPub are both sides' public keys in protocol order.
func (k *kex) derive(peerB64 string, oldSide bool) (key []byte, code string, err error) {
	peer, err := b64.DecodeString(peerB64)
	if err != nil {
		return nil, "", errors.New("bad key from the other device")
	}
	pk, err := ecdh.P256().NewPublicKey(peer)
	if err != nil {
		return nil, "", errors.New("bad key from the other device")
	}
	shared, err := k.priv.ECDH(pk)
	if err != nil {
		return nil, "", err
	}
	oldPub, newPub := k.pub, peer
	if !oldSide {
		oldPub, newPub = peer, k.pub
	}
	salt := sha256.Sum256(append(append([]byte{}, oldPub...), newPub...))
	key, err = hkdf.Key(sha256.New, shared, salt[:], "tjena-bridge move key", 32)
	if err != nil {
		return nil, "", err
	}
	s, err := hkdf.Key(sha256.New, shared, salt[:], "tjena-bridge move sas", 4)
	if err != nil {
		return nil, "", err
	}
	return key, fmt.Sprintf("%06d", binary.BigEndian.Uint32(s)%1_000_000), nil
}
