package core

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/flynn/noise"
)

// cipherSuite is the single algorithm choice for every Noise session.
//
// ChaChaPoly is chosen over AES-GCM because ChaCha is constant-time in
// software; the desktop and phone may not have AES hardware. BLAKE2b is
// the recommended pairing for 25519 in the Noise spec.
var cipherSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2b)

// dh25519KeyLen is the wire length of a Curve25519 public or private key.
const dh25519KeyLen = 32

// NoiseSession holds the per-session state after a Noise IK handshake.
//
// IK is chosen because the phone learns the desktop's static key from the
// pairing QR, which is already an authenticated channel. Knowing the remote
// static key up front gives a one round trip handshake and hides the
// initiator's identity from any observer, including the relay.
//
// The two CipherStates returned by a completed handshake are directional:
//   - the initiator uses cs1 to encrypt (phone→desktop) and cs2 to decrypt
//     (desktop→phone)
//   - the responder uses cs1 to decrypt (phone→desktop) and cs2 to encrypt
//     (desktop→phone)
//
// Seal and Open pick the correct state per role so callers do not need to
// know which direction they are. Both cipher states advance their nonces
// independently, so replies cannot collide with requests on the same channel.
type NoiseSession struct {
	// mu guards every operation on the session, not just field access.
	//
	// flynn/noise advances a nonce counter inside Encrypt and Decrypt, so two
	// goroutines sealing at once would reuse a ChaCha20-Poly1305 nonce, which
	// leaks the keystream and lets frames be forged. Android reaches the core
	// from the UI thread and the stream reader at the same time, so this is a
	// real path, not a theoretical one.
	mu sync.Mutex

	initiator bool

	// hs is non-nil only until the handshake completes.
	hs *noise.HandshakeState

	// After the handshake, cs1 and cs2 carry the directional cipher states.
	// The mapping to send/recv is role-dependent; see Seal and Open.
	cs1 *noise.CipherState
	cs2 *noise.CipherState
}

// NewDesktopKeypair generates a fresh static Curve25519 keypair for the
// desktop. The public half is embedded in the pairing QR; the private half
// stays on disk.
func NewDesktopKeypair() (noise.DHKey, error) {
	return cipherSuite.GenerateKeypair(nil)
}

// DesktopPublicKey returns the base64-encoded public key from a keypair.
// This is the value that goes into the pairing QR and is passed to
// NewInitiatorSession on the phone.
func DesktopPublicKey(kp noise.DHKey) string {
	return base64.StdEncoding.EncodeToString(kp.Public)
}

// EncodeKeypair encodes a keypair as JSON containing base64-encoded public and
// private keys, so it survives being written to disk on the desktop or to
// the Android Keystore as an opaque blob.
func EncodeKeypair(kp noise.DHKey) string {
	v := struct {
		Public  string `json:"public"`
		Private string `json:"private"`
	}{
		Public:  base64.StdEncoding.EncodeToString(kp.Public),
		Private: base64.StdEncoding.EncodeToString(kp.Private),
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// DecodeKeypair is the inverse of EncodeKeypair. It rejects anything that does
// not decode to a valid 32-byte Curve25519 keypair.
func DecodeKeypair(encoded string) (noise.DHKey, error) {
	var v struct {
		Public  string `json:"public"`
		Private string `json:"private"`
	}
	if err := json.Unmarshal([]byte(encoded), &v); err != nil {
		return noise.DHKey{}, fmt.Errorf("decode keypair: %w", err)
	}
	pub, err := base64.StdEncoding.DecodeString(v.Public)
	if err != nil || len(pub) != dh25519KeyLen {
		return noise.DHKey{}, errors.New("decode keypair: invalid public key")
	}
	priv, err := base64.StdEncoding.DecodeString(v.Private)
	if err != nil || len(priv) != dh25519KeyLen {
		return noise.DHKey{}, errors.New("decode keypair: invalid private key")
	}
	return noise.DHKey{Public: pub, Private: priv}, nil
}

// NewResponderSession creates the desktop side of a session. The desktop waits
// on the relay for whoever arrives; it does not know the phone's static key
// ahead of time, which is the responder role in the IK pattern.
func NewResponderSession(kp noise.DHKey) (*NoiseSession, error) {
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   cipherSuite,
		Pattern:       noise.HandshakeIK,
		Initiator:     false,
		StaticKeypair: kp,
	})
	if err != nil {
		return nil, fmt.Errorf("new responder session: %w", err)
	}
	return &NoiseSession{initiator: false, hs: hs}, nil
}

// NewInitiatorSession creates the phone side of a session. desktopPublicKey
// must be the base64-encoded static public key of the desktop the phone paired
// with. Knowing it up front is what allows the IK handshake to complete in one
// round trip and keeps the phone's identity hidden from the relay.
func NewInitiatorSession(desktopPublicKey string) (*NoiseSession, error) {
	pub, err := base64.StdEncoding.DecodeString(desktopPublicKey)
	if err != nil {
		return nil, errors.New("new initiator session: desktop key is not valid base64")
	}
	if len(pub) != dh25519KeyLen {
		return nil, fmt.Errorf("new initiator session: desktop key must be %d bytes, got %d", dh25519KeyLen, len(pub))
	}

	// The initiator also needs its own ephemeral keypair, which the library
	// generates from the system CSRNG when Random is nil.
	myKey, err := cipherSuite.GenerateKeypair(nil)
	if err != nil {
		return nil, fmt.Errorf("new initiator session: generate ephemeral key: %w", err)
	}

	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   cipherSuite,
		Pattern:       noise.HandshakeIK,
		Initiator:     true,
		StaticKeypair: myKey,
		PeerStatic:    pub,
	})
	if err != nil {
		return nil, fmt.Errorf("new initiator session: %w", err)
	}
	return &NoiseSession{initiator: true, hs: hs}, nil
}

// Established reports whether the handshake has completed and the session is
// ready to carry application data.
func (s *NoiseSession) Established() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.established()
}

// established is the lock-free form for callers that already hold s.mu.
func (s *NoiseSession) established() bool {
	return s.cs1 != nil && s.cs2 != nil
}

// StartHandshake produces the initiator's first (and only) handshake message.
// Called by the phone after connecting to the relay.
func (s *NoiseSession) StartHandshake() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.initiator {
		return nil, errors.New("StartHandshake called on the responder side")
	}
	if s.hs == nil {
		return nil, errors.New("handshake already completed")
	}
	msg, cs1, cs2, err := s.hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("start handshake: %w", err)
	}
	if cs1 != nil && cs2 != nil {
		// IK: the initiator is done after the first write.
		s.cs1, s.cs2 = cs1, cs2
		s.hs = nil
	}
	return msg, nil
}

// ReadHandshake is the responder's side: it reads the initiator's message and
// returns its own reply. The session becomes established after this call.
func (s *NoiseSession) ReadHandshake(msg []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.initiator {
		return nil, errors.New("ReadHandshake called on the initiator side")
	}
	if s.hs == nil {
		return nil, errors.New("handshake already completed")
	}
	_, cs1, cs2, err := s.hs.ReadMessage(nil, msg)
	if err != nil {
		return nil, fmt.Errorf("read handshake: %w", err)
	}

	// Write the responder's reply (second message in IK).
	reply, cs1w, cs2w, err := s.hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("write handshake reply: %w", err)
	}
	// Pick up cipher states from whichever message finalised them.
	if cs1 == nil && cs1w != nil {
		cs1, cs2 = cs1w, cs2w
	}
	if cs1 != nil && cs2 != nil {
		s.cs1, s.cs2 = cs1, cs2
		s.hs = nil
	}
	return reply, nil
}

// FinishHandshake is the initiator's final step: it reads the responder's
// reply and completes the session if the responder authenticates.
func (s *NoiseSession) FinishHandshake(reply []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.initiator {
		return errors.New("FinishHandshake called on the responder side")
	}
	// Deliberately no early return for an already-established session. In IK
	// the initiator is not authenticated to the responder until it has read
	// this reply, so treating an established session as "nothing to do" would
	// let a relay that never proved it holds the desktop key be accepted.
	if s.hs == nil {
		return errors.New("no handshake in progress")
	}
	_, cs1, cs2, err := s.hs.ReadMessage(nil, reply)
	if err != nil {
		return fmt.Errorf("finish handshake: %w", err)
	}
	if cs1 != nil && cs2 != nil {
		s.cs1, s.cs2 = cs1, cs2
		s.hs = nil
	}
	return nil
}

// Seal encrypts plaintext for the remote peer. The correct directional cipher
// state is chosen by role, so the phone's Seal and the desktop's Seal use
// independent nonces and do not collide.
func (s *NoiseSession) Seal(plaintext []byte) ([]byte, error) {
	// Held across Encrypt, not merely around the field reads: the nonce lives
	// inside the CipherState and advances during the call.
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.established() {
		return nil, errors.New("Seal called before the handshake completed")
	}
	// Initiator sends on cs1; responder sends on cs2.
	// See the type comment for the full mapping.
	var sender *noise.CipherState
	if s.initiator {
		sender = s.cs1
	} else {
		sender = s.cs2
	}
	ct, err := sender.Encrypt(nil, nil, plaintext)
	if err != nil {
		return nil, fmt.Errorf("seal: %w", err)
	}
	return ct, nil
}

// Open decrypts a frame that was sealed by the remote peer. Tampered or
// replayed frames are rejected by the AEAD tag check or the advancing nonce.
func (s *NoiseSession) Open(frame []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.established() {
		return nil, errors.New("Open called before the handshake completed")
	}
	// Initiator receives on cs2; responder receives on cs1.
	var receiver *noise.CipherState
	if s.initiator {
		receiver = s.cs2
	} else {
		receiver = s.cs1
	}
	pt, err := receiver.Decrypt(nil, nil, frame)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	return pt, nil
}

// EncodeFrame encodes raw frame bytes to a base64 string that is safe to
// pass across the string-only gomobile FFI boundary (no NUL, no newlines).
func EncodeFrame(frame []byte) string {
	return base64.StdEncoding.EncodeToString(frame)
}

// DecodeFrame is the inverse of EncodeFrame.
func DecodeFrame(encoded string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode frame: %w", err)
	}
	return b, nil
}

// sessionRegistry maps integer handles to sessions for gomobile callers.
//
// gomobile cannot pass Go pointers across the FFI boundary, so callers on the
// Android side hold an opaque integer and the registry keeps the real pointer.
// The mutex guards concurrent calls from different Android threads.
var (
	sessionMu     sync.Mutex
	sessionMap    = map[int]*NoiseSession{}
	sessionNextID = 1
)

// OpenInitiatorSession creates an initiator session and returns an opaque
// handle for gomobile callers.
func OpenInitiatorSession(desktopPublicKey string) (int, error) {
	s, err := NewInitiatorSession(desktopPublicKey)
	if err != nil {
		return 0, err
	}
	sessionMu.Lock()
	defer sessionMu.Unlock()
	id := sessionNextID
	sessionNextID++
	sessionMap[id] = s
	return id, nil
}

// CloseSession releases a session handle. After this call the handle must not
// be used; any further operation on it will return an error rather than panic.
func CloseSession(handle int) {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	delete(sessionMap, handle)
}

// lookupSession returns the session for a handle, or an error if the handle is
// unknown or has been closed. Errors here propagate across FFI, where a panic
// would bring the entire app down.
func lookupSession(handle int) (*NoiseSession, error) {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	s, ok := sessionMap[handle]
	if !ok {
		return nil, fmt.Errorf("session handle %d is unknown or has been closed", handle)
	}
	return s, nil
}

// SessionStartHandshake is the gomobile-friendly wrapper for StartHandshake.
// The returned string is base64 so it survives the string-only FFI boundary.
func SessionStartHandshake(handle int) (string, error) {
	s, err := lookupSession(handle)
	if err != nil {
		return "", err
	}
	msg, err := s.StartHandshake()
	if err != nil {
		return "", err
	}
	return EncodeFrame(msg), nil
}

// SessionFinishHandshake is the gomobile-friendly wrapper for FinishHandshake.
// reply is expected to be a base64-encoded frame from EncodeFrame.
func SessionFinishHandshake(handle int, reply string) error {
	s, err := lookupSession(handle)
	if err != nil {
		return err
	}
	raw, err := DecodeFrame(reply)
	if err != nil {
		return err
	}
	return s.FinishHandshake(raw)
}

// SessionSeal is the gomobile-friendly wrapper for Seal. Both the plaintext
// input and the ciphertext output are plain strings; the output is base64.
func SessionSeal(handle int, plaintext string) (string, error) {
	s, err := lookupSession(handle)
	if err != nil {
		return "", err
	}
	ct, err := s.Seal([]byte(plaintext))
	if err != nil {
		return "", err
	}
	return EncodeFrame(ct), nil
}

// SessionOpen is the gomobile-friendly wrapper for Open, and the reason the
// phone can read replies at all. frame is base64 from EncodeFrame; the returned
// plaintext is a plain string.
//
// Only the initiator side crosses the FFI: the responder is the desktop, which
// is also Go and calls NoiseSession directly. That is why there is no
// SessionReadHandshake counterpart.
func SessionOpen(handle int, frame string) (string, error) {
	s, err := lookupSession(handle)
	if err != nil {
		return "", err
	}
	raw, err := DecodeFrame(frame)
	if err != nil {
		return "", err
	}
	pt, err := s.Open(raw)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// SessionEstablished reports whether a handle's session is ready to carry
// application data. An unknown or closed handle is simply not established,
// which is the honest answer and avoids an error return for a predicate.
func SessionEstablished(handle int) bool {
	s, err := lookupSession(handle)
	if err != nil {
		return false
	}
	return s.Established()
}
