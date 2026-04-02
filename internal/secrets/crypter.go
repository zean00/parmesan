package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
)

type Crypter struct {
	key []byte
}

func New(masterKey string) *Crypter {
	if masterKey == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(masterKey))
	return &Crypter{key: sum[:]}
}

func (c *Crypter) Encrypt(plaintext string) (string, error) {
	if c == nil {
		return "", errors.New("crypter unavailable")
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (c *Crypter) Decrypt(ciphertext string) (string, error) {
	if c == nil {
		return "", errors.New("crypter unavailable")
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce := raw[:gcm.NonceSize()]
	plaintext, err := gcm.Open(nil, nonce, raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
