package e2e

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
)

const nonceSize = 12

type Codec struct {
	gcm cipher.AEAD
}

func NewCodec(sharedKey string) (*Codec, error) {
	trimmed := strings.TrimSpace(sharedKey)
	if trimmed == "" {
		return nil, fmt.Errorf("missing E2E key")
	}

	key := sha256.Sum256([]byte("shadow-e2e-v1:" + trimmed))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("failed to init E2E cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to init E2E mode: %w", err)
	}
	return &Codec{gcm: gcm}, nil
}

func (c *Codec) Encrypt(plaintext []byte) (string, error) {
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate E2E nonce: %w", err)
	}

	sealed := c.gcm.Seal(nil, nonce, plaintext, nil)
	payload := append(nonce, sealed...)
	return base64.StdEncoding.EncodeToString(payload), nil
}

func (c *Codec) Decrypt(encoded string) ([]byte, error) {
	payload, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid E2E payload encoding: %w", err)
	}
	if len(payload) < nonceSize {
		return nil, fmt.Errorf("invalid E2E payload size")
	}

	nonce := payload[:nonceSize]
	ciphertext := payload[nonceSize:]
	plaintext, err := c.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt E2E payload: %w", err)
	}
	return plaintext, nil
}

func GenerateShareKey() (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("failed to generate share key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
