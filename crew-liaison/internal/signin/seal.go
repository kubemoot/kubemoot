package signin

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Purposes bind a sealed blob to the one place it may be opened, so a client id cannot be
// presented as a code or a state.
const (
	purposeClient  = "client"
	purposeState   = "state"
	purposeCode    = "code"
	purposeDoor    = "door"
	purposeRefresh = "refresh"
	// purposeStored is the prefix of the purpose of a value kept in the token store; the
	// store key follows it, so a value copied under another person's key does not open.
	purposeStored = "stored:"
)

const sealVersion byte = 1

var (
	errBadBlob = errors.New("sealed value is not valid")
	keyIDForm  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
)

// Sealer encrypts small values into strings the front hands out and later opens again, so
// it keeps nothing. Each blob names the key that sealed it, so keys rotate: the first key
// seals, every listed key opens.
type Sealer struct {
	active string
	keys   map[string]cipher.AEAD
}

// ParseKeys reads a key list: one "id:base64key" entry per line or comma, each key 32 bytes.
// The first entry seals new blobs; the others only open older ones.
func ParseKeys(spec string) (*Sealer, error) {
	s := &Sealer{keys: map[string]cipher.AEAD{}}
	for _, entry := range strings.FieldsFunc(spec, func(r rune) bool { return r == '\n' || r == ',' }) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, aead, err := parseKey(entry)
		if err != nil {
			return nil, err
		}
		if _, dup := s.keys[id]; dup {
			return nil, fmt.Errorf("seal key id %q is listed twice", id)
		}
		if s.active == "" {
			s.active = id
		}
		s.keys[id] = aead
	}
	if s.active == "" {
		return nil, errors.New("no seal key given")
	}
	return s, nil
}

func parseKey(entry string) (string, cipher.AEAD, error) {
	id, encoded, ok := strings.Cut(entry, ":")
	if !ok || !keyIDForm.MatchString(id) {
		return "", nil, errors.New(`a seal key must be "id:base64key" with an id of letters, digits, "_", "." or "-"`)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(raw) != 32 {
		return "", nil, fmt.Errorf("seal key %q must be 32 bytes, base64 encoded", id)
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return "", nil, err
	}
	aead, err := cipher.NewGCM(block)
	return id, aead, err
}

// Seal encrypts v as JSON under the active key. The result is URL safe.
func (s *Sealer) Seal(purpose string, v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	aead := s.keys[s.active]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := []byte{sealVersion, byte(len(s.active))}
	out = append(out, s.active...)
	out = append(out, nonce...)
	out = aead.Seal(out, nonce, plain, []byte(purpose))
	return base64.RawURLEncoding.EncodeToString(out), nil
}

// Open decrypts blob into v. Any malformation, unknown key, wrong purpose or tampering is
// errBadBlob, and the cause is not distinguished.
func (s *Sealer) Open(purpose, blob string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(blob)
	if err != nil || len(raw) < 2 || raw[0] != sealVersion {
		return errBadBlob
	}
	idLen := int(raw[1])
	if len(raw) < 2+idLen {
		return errBadBlob
	}
	aead, ok := s.keys[string(raw[2:2+idLen])]
	if !ok {
		return errBadBlob
	}
	rest := raw[2+idLen:]
	if len(rest) < aead.NonceSize() {
		return errBadBlob
	}
	plain, err := aead.Open(nil, rest[:aead.NonceSize()], rest[aead.NonceSize():], []byte(purpose))
	if err != nil || json.Unmarshal(plain, v) != nil {
		return errBadBlob
	}
	return nil
}
