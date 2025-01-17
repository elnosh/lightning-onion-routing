package lnonion

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/chacha20"
)

var FinalHop = errors.New("final destination of onion")

func (h HopPayload) Size() int {
	// 1 byte for the encoded payload length (assuming length is < 255)
	// plus actual bytes of payload + 32 bytes for the hmac
	return 1 + len(h.Payload) + 32
}

type HopPayload struct {
	PublicKey *secp256k1.PublicKey
	Payload   []byte
}

type Onion struct {
	Version     byte
	Point       [33]byte
	HopPayloads [1300]byte
	Hmac        [32]byte
}

func (o Onion) Serialize() []byte {
	var packet = [1366]byte{o.Version}
	copy(packet[1:34], o.Point[:])
	copy(packet[34:1334], o.HopPayloads[:])
	copy(packet[1334:], o.Hmac[:])
	return packet[:]
}

func DeserializeOnion(b []byte) (*Onion, error) {
	if len(b) != 1366 {
		return nil, errors.New("onion must be 1366 bytes")
	}

	onion := &Onion{}
	onion.Version = b[0]
	copy(onion.Point[:], b[1:34])
	copy(onion.HopPayloads[:], b[34:1334])
	copy(onion.Hmac[:], b[1334:])

	return onion, nil
}

func ConstructOnion(sessionKey *secp256k1.PrivateKey, hops []HopPayload) (*Onion, error) {
	numHops := len(hops)
	ephemeralPublicKeys := make([]*secp256k1.PublicKey, numHops)
	sharedSecrets := make([][]byte, numHops)
	blindingFactors := make([]*secp256k1.PrivateKey, numHops)

	currentkey := sessionKey

	// first need to compute the necessary keys to then construct the onion
	for i, hop := range hops {
		// for each hop need to compute a shared secret and the ephemeral key for the next hop
		ephemeralPublicKeys[i] = currentkey.PubKey()

		// shared secret is computed by doing ECDH exchange with the current ephemeral private key
		// and the hop's public key and then hashing (SHA256) that
		// NOTE: node peeling the onion at this hop will derive the shared secret
		// by doing the reverse i.e ECDH using his own private key and the ephemeral public key
		var pkpoint, ecdhpoint secp256k1.JacobianPoint
		hop.PublicKey.AsJacobian(&pkpoint)
		secp256k1.ScalarMultNonConst(&currentkey.Key, &pkpoint, &ecdhpoint)
		ecdhpoint.ToAffine()
		ecdhkey := secp256k1.NewPublicKey(&ecdhpoint.X, &ecdhpoint.Y)
		sharedSecret := sha256.Sum256(ecdhkey.SerializeCompressed())

		// the ephemeral private key for the next hop is computed by multiplying
		// the current ephemeral private key and a blinding factor
		// the blinding factor is the SHA256 of concatenating the current ephemeral public key and the shared secret
		blindingFactorHash := sha256.Sum256(append(currentkey.PubKey().SerializeCompressed(), sharedSecret[:]...))
		blindingFactor := secp256k1.PrivKeyFromBytes(blindingFactorHash[:])
		currentkey.Key.Mul(&blindingFactor.Key)

		sharedSecrets[i] = sharedSecret[:]
		blindingFactors[i] = blindingFactor
	}

	// initialize packet with 1300 random bytes
	padKey := generateKey(pad, sharedSecrets[0])
	packetBytes := generateRandomByteStream(padKey, 1300)
	nextHmac := make([]byte, 32)

	filler := generateFiller(hops, sharedSecrets)

	// packet construction is done backwards
	for i := numHops - 1; i >= 0; i-- {
		// used for generating pseudo-random byte stream to obfuscate (by xor-ing) payload at each hop
		rhoKey := generateKey(rho, sharedSecrets[i])
		// used to generate hmac
		muKey := generateKey(mu, sharedSecrets[i])

		hopPayloadLength := len(hops[i].Payload)
		shiftSize := hops[i].Size()

		hopPayload := make([]byte, 1, shiftSize)
		// NOTE: this length is wrong, should be bigsize encoding.
		hopPayload[0] = byte(hopPayloadLength)
		hopPayload = append(hopPayload, hops[i].Payload...)
		hopPayload = append(hopPayload, nextHmac...)

		rightShift(packetBytes, shiftSize)
		copy(packetBytes[:], hopPayload)

		// pseudo-random byte stream xor'd with `hop_payloads`
		byteStream := generateRandomByteStream(rhoKey, 1300)
		xor(packetBytes, packetBytes, byteStream)

		if i == numHops-1 {
			copy(packetBytes[len(packetBytes)-len(filler):], filler)
		}

		hmac := hmac.New(sha256.New, muKey)
		hmac.Write(packetBytes)
		nextHmac = hmac.Sum(nil)
	}

	var publickey [33]byte
	copy(publickey[:], ephemeralPublicKeys[0].SerializeCompressed())

	var hopPayloads [1300]byte
	copy(hopPayloads[:], packetBytes)

	var hmac [32]byte
	copy(hmac[:], nextHmac)

	return &Onion{
		Version:     0x00,
		Point:       publickey,
		HopPayloads: hopPayloads,
		Hmac:        hmac,
	}, nil
}

func ProcessOnion(onion *Onion, hopPrivateKey *secp256k1.PrivateKey) (*HopPayload, *Onion, error) {
	if onion.Version != 0x00 {
		return nil, nil, errors.New("incorrect version")
	}

	// ephemeral public key that will be used for deriving the shared secret
	pubkey, err := secp256k1.ParsePubKey(onion.Point[:])
	if err != nil {
		return nil, nil, fmt.Errorf("invalid public key: %v", err)
	}

	// shared secret is computed by doing ECDH exchange with the ephemeral public key
	// and the hop's private key and then hashing (SHA256) that
	// NOTE: the origin node did the reverse i.e it calculated the shared
	// secret by doing ECDH using the ephemeral private key and this hop's public key
	var pubkeypoint, ecdhpoint secp256k1.JacobianPoint
	pubkey.AsJacobian(&pubkeypoint)
	secp256k1.ScalarMultNonConst(&hopPrivateKey.Key, &pubkeypoint, &ecdhpoint)
	ecdhpoint.ToAffine()
	ecdhkey := secp256k1.NewPublicKey(&ecdhpoint.X, &ecdhpoint.Y)
	sharedSecret := sha256.Sum256(ecdhkey.SerializeCompressed())

	// derive hmac and compare with hmac in onion
	muKey := generateKey(mu, sharedSecret[:])
	h := hmac.New(sha256.New, muKey)
	h.Write(onion.HopPayloads[:])
	hmacBytes := h.Sum(nil)
	if !hmac.Equal(hmacBytes, onion.Hmac[:]) {
		return nil, nil, errors.New("invalid hmac")
	}

	// derive bytestream which will then be xor'd with the payload
	// that will decrypt only the intended payload for this hop.
	rhoKey := generateKey(rho, sharedSecret[:])
	byteStream := generateRandomByteStream(rhoKey, 2600)

	// before doing the xor with generated byte stream
	// need to pad the hop payload with 1300 zero bytes
	var unwrappedPayloads [2600]byte
	copy(unwrappedPayloads[:], onion.HopPayloads[:])
	xor(unwrappedPayloads[:], unwrappedPayloads[:], byteStream)

	// this length should be encoded
	payloadLength := unwrappedPayloads[0]
	if payloadLength < 2 {
		return nil, nil, errors.New("payload length too short")
	}

	payload := make([]byte, payloadLength)
	copy(payload, unwrappedPayloads[1:payloadLength+1])

	hopPayload := &HopPayload{
		PublicKey: hopPrivateKey.PubKey(),
		Payload:   payload,
	}

	nextHmac := unwrappedPayloads[1+payloadLength : 1+payloadLength+32]
	zeroslice := make([]byte, 32)
	// if nextHmac is all-zero, then this is the final destination, congrats
	if bytes.Compare(zeroslice, nextHmac) == 0 {
		return hopPayload, nil, FinalHop
	}

	// derive blinding factor which is the SHA256 of the ephemeral public key and the shared secret
	blindingFactor := sha256.Sum256(append(pubkey.SerializeCompressed(), sharedSecret[:]...))
	blindingFactorKey := secp256k1.PrivKeyFromBytes(blindingFactor[:])

	// public key for the next hop is the current ephemeral public key
	// multiplied by the blinding factor
	var nextPublicKeyPoint secp256k1.JacobianPoint
	secp256k1.ScalarMultNonConst(&blindingFactorKey.Key, &pubkeypoint, &nextPublicKeyPoint)
	nextPublicKeyPoint.ToAffine()
	nextPublicKey := secp256k1.NewPublicKey(&nextPublicKeyPoint.X, &nextPublicKeyPoint.Y)

	var publicKey [33]byte
	copy(publicKey[:], nextPublicKey.SerializeCompressed())

	var nextHopPayloads [1300]byte
	copy(nextHopPayloads[:], unwrappedPayloads[1+payloadLength+32:])

	var hmac [32]byte
	copy(hmac[:], nextHmac)

	// onion to send to next hop in route
	nextHopOnion := &Onion{
		Version:     0x00,
		Point:       publicKey,
		HopPayloads: nextHopPayloads,
		Hmac:        hmac,
	}
	return hopPayload, nextHopOnion, nil
}

// each hop needs to decrypt the routing information intended for it
// but they need to generate more random data to obfuscate how far in
// the route is the packet
// generateFiller will be used by the origin node (sending)
// to generate the filler that will be generated by each hop
// so that the HMACs are computed and verified correctly
func generateFiller(hops []HopPayload, sharedSecrets [][]byte) []byte {
	fillerSize := 0
	// do not calculate for the last hop since it does not need to generate the HMAC
	for i := 0; i < len(hops)-1; i++ {
		fillerSize += hops[i].Size()
	}
	filler := make([]byte, fillerSize)

	for i := 0; i < len(hops)-1; i++ {
		// the difference between fillerEnd and fillerStart
		// is the number of bytes from the onion that have been "processed" until this hop
		// so that is the number of bytes that the current hop will obfuscate
		// while decrypting
		fillerStart := 1300
		for _, hop := range hops[:i] {
			fillerStart -= hop.Size()
		}
		fillerEnd := 1300 + hops[i].Size()

		rhoKey := generateKey(rho, sharedSecrets[i])
		byteStream := generateRandomByteStream(rhoKey, 2600)

		xor(filler, filler, byteStream[fillerStart:fillerEnd])
	}

	return filler
}

func generateRandomByteStream(key []byte, numBytes int) []byte {
	// 96-bit zero-nonce
	nonce := make([]byte, 12)
	byteStream := make([]byte, numBytes)
	cipher, err := chacha20.NewUnauthenticatedCipher(key, nonce)
	if err != nil {
		panic(err)
	}

	cipher.XORKeyStream(byteStream, byteStream)
	return byteStream
}

var (
	rho = []byte{0x72, 0x68, 0x6f}
	mu  = []byte{0x6d, 0x75}
	um  = []byte{0x75, 0x6d}
	pad = []byte{0x70, 0x61, 0x64}
)

// generate keys that will be used for encryption and verification:
// - rho
// - mu
// - um
// - pad
func generateKey(keyType []byte, secret []byte) []byte {
	hmac := hmac.New(sha256.New, keyType)
	hmac.Write(secret)
	key := hmac.Sum(nil)
	return key
}

// xor computes the byte wise XOR of a and b, storing the result in dst. Only
// the first `min(len(a), len(b))` bytes will be xor'd.
func xor(dst, a, b []byte) {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}

	for i := 0; i < n; i++ {
		dst[i] = a[i] ^ b[i]
	}
}

// rightShift shifts the byte-slice by the given number of bytes to the right
// and 0-fill the resulting gap.
func rightShift(slice []byte, num int) {
	for i := len(slice) - num - 1; i >= 0; i-- {
		slice[num+i] = slice[i]
	}

	for i := 0; i < num; i++ {
		slice[i] = 0
	}
}
