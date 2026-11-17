package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	securityMasterKeyEnv = "SECURITY_MASTER_KEY"
	encryptedPrefix      = "sec:v1:"
)

func Encrypt(plainText string) (string, error) {
	key, err := masterKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	cipherBytes := gcm.Seal(nil, nonce, []byte(plainText), nil)
	payload := base64.RawURLEncoding.EncodeToString(nonce) + ":" + base64.RawURLEncoding.EncodeToString(cipherBytes)
	return encryptedPrefix + payload, nil
}

func Decrypt(cipherText string) (string, error) {
	raw := strings.TrimSpace(cipherText)
	if raw == "" {
		return "", nil
	}
	if !strings.HasPrefix(raw, encryptedPrefix) {
		// Backward compatibility for legacy plain-text rows.
		return raw, nil
	}

	body := strings.TrimPrefix(raw, encryptedPrefix)
	parts := strings.Split(body, ":")
	if len(parts) != 2 {
		return "", errors.New("invalid encrypted payload format")
	}

	key, err := masterKey()
	if err != nil {
		return "", err
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("decode nonce: %w", err)
	}
	cipherBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode cipher text: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, nonce, cipherBytes, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func masterKey() ([]byte, error) {
	configured := strings.TrimSpace(os.Getenv(securityMasterKeyEnv))
	if configured == "" {
		return nil, fmt.Errorf("%s is required to encrypt or decrypt stored credentials", securityMasterKeyEnv)
	}
	if len(configured) != 32 {
		return nil, fmt.Errorf("%s must have exactly 32 bytes", securityMasterKeyEnv)
	}
	return []byte(configured), nil
}
