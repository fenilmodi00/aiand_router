package auth

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"
	"github.com/tink-crypto/tink-go/v2/tink"
)

// Encryptor encrypts/decrypts secrets via AES-256-GCM.
// AAD binds each ciphertext to (externalID, purpose). Existing external API
// keys and refresh tokens use the provider as their purpose; subscription
// access tokens use a distinct purpose suffix.
type Encryptor interface {
	Encrypt(plaintext []byte, externalID, provider string) (ciphertext []byte, err error)
	Decrypt(ciphertext []byte, externalID, provider string) (plaintext []byte, err error)
}

type tinkEncryptor struct {
	aead tink.AEAD
}

// NewTinkEncryptor creates an Encryptor from a Tink keyset JSON. The keyset
// may alternatively be supplied base64-encoded (standard encoding): some
// config-var stores mangle embedded double quotes in raw JSON, so operators
// can round-trip the keyset through base64 to survive them. Values that
// already parse as keyset JSON are used verbatim.
func NewTinkEncryptor(keysetJSON string) (Encryptor, error) {
	if !strings.HasPrefix(strings.TrimSpace(keysetJSON), "{") {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keysetJSON))
		if err != nil {
			return nil, fmt.Errorf("decode base64 keyset: %w", err)
		}
		keysetJSON = string(decoded)
	}
	reader := keyset.NewJSONReader(bytes.NewBufferString(keysetJSON))
	handle, err := insecurecleartextkeyset.Read(reader)
	if err != nil {
		return nil, fmt.Errorf("read keyset: %w", err)
	}
	primitive, err := aead.New(handle)
	if err != nil {
		return nil, fmt.Errorf("create AEAD primitive: %w", err)
	}
	return &tinkEncryptor{aead: primitive}, nil
}

func (e *tinkEncryptor) Encrypt(plaintext []byte, externalID, provider string) ([]byte, error) {
	return e.aead.Encrypt(plaintext, aadFor(externalID, provider))
}

func (e *tinkEncryptor) Decrypt(ciphertext []byte, externalID, provider string) ([]byte, error) {
	return e.aead.Decrypt(ciphertext, aadFor(externalID, provider))
}

// aadFor MUST stay byte-identical with the Aiand-side helper at
// backend/internal/app/aiandrouter/crypto.go.
func aadFor(externalID, provider string) []byte {
	return []byte(externalID + "\x00" + provider)
}

// NoOpEncryptor is a pass-through for development without encryption.
type NoOpEncryptor struct{}

func (NoOpEncryptor) Encrypt(plaintext []byte, _, _ string) ([]byte, error) {
	return plaintext, nil
}

func (NoOpEncryptor) Decrypt(ciphertext []byte, _, _ string) ([]byte, error) {
	return ciphertext, nil
}
