package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// sealer encrypts and authenticates cookie values with AES-256-GCM. Each cookie's name is the
// additional data, so a value can't be moved from one cookie to another.
type sealer struct{ aead cipher.AEAD }

// newSealer reads the key file: 32 bytes, raw or base64 (standard or URL, padded or not), so it can
// come straight from a Secret.
func newSealer(keyFile string) (*sealer, error) {
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	key := raw
	if len(key) != 32 {
		text := strings.TrimSpace(string(raw))
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			if k, err := enc.DecodeString(text); err == nil && len(k) == 32 {
				key = k
				break
			}
		}
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s: the session key must be 32 bytes (raw or base64)", keyFile)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &sealer{aead}, nil
}

func (s *sealer) seal(name string, v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, plain, []byte(name))), nil
}

var errBadCookie = errors.New("cookie is not valid")

func (s *sealer) open(name, value string, v any) error {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(data) < s.aead.NonceSize() {
		return errBadCookie
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, data[:n], data[n:], []byte(name))
	if err != nil {
		return errBadCookie
	}
	if json.Unmarshal(plain, v) != nil {
		return errBadCookie
	}
	return nil
}
