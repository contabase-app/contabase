package security

import (
	"strconv"
	"strings"
	"testing"
)

// AES-256-GCM fixture made with the former development seed and a synthetic secret.
const legacySMTPFixture = "sec:v1:AAECAwQFBgcICQoL:NLiC-eMkHgguaU-W8QZcl_wHP1xR5QIBnrG6gu0oMJW2CpwD1A"
const syntheticMasterKey = "0123456789abcdef0123456789abcdef"

func TestMasterKeyRequiresExplicitConfigurationInEveryEnvironment(t *testing.T) {
	for _, appEnv := range []string{"", "development", "production", "staging"} {
		t.Run("env="+appEnv, func(t *testing.T) {
			t.Setenv("APP_ENV", appEnv)
			for _, key := range []string{"", " \t\n", "invalid-synthetic-key", strings.Repeat("x", 33)} {
				t.Run("bytes="+strconv.Itoa(len(key)), func(t *testing.T) {
					t.Setenv(securityMasterKeyEnv, key)
					for _, operation := range []struct {
						name  string
						call  func(string) (string, error)
						input string
					}{
						{"encrypt", Encrypt, "synthetic-password"},
						{"decrypt", Decrypt, legacySMTPFixture},
					} {
						out, err := operation.call(operation.input)
						if err == nil || out != "" {
							t.Fatalf("%s must fail without returning credentials", operation.name)
						}
						if !strings.Contains(err.Error(), securityMasterKeyEnv) {
							t.Fatalf("%s must identify the configuration variable", operation.name)
						}
						if strings.TrimSpace(key) != "" && strings.Contains(err.Error(), key) {
							t.Fatal("configuration error disclosed the key")
						}
					}
				})
			}
		})
	}
}

func TestEncryptDecryptWithExplicitKey(t *testing.T) {
	for _, appEnv := range []string{"development", "production"} {
		t.Run(appEnv, func(t *testing.T) {
			t.Setenv("APP_ENV", appEnv)
			// Existing 32-byte keys need not be hex encoded.
			for _, key := range []string{syntheticMasterKey, strings.Repeat("K", 32)} {
				t.Setenv(securityMasterKeyEnv, key)
				for _, plain := range []string{"", "synthetic-smtp-password", "senha-sintética"} {
					cipherText, err := Encrypt(plain)
					if err != nil {
						t.Fatalf("encrypt: %v", err)
					}
					if !strings.HasPrefix(cipherText, "sec:v1:") || (plain != "" && strings.Contains(cipherText, plain)) {
						t.Fatal("expected the existing encrypted format without plaintext")
					}
					got, err := Decrypt(cipherText)
					if err != nil || got != plain {
						t.Fatalf("round trip failed: %v", err)
					}
					second, err := Encrypt(plain)
					if err != nil || second == cipherText {
						t.Fatal("encrypt must use a fresh nonce")
					}
				}
			}
		})
	}
}

func TestDecryptRejectsWrongKeyAndTampering(t *testing.T) {
	t.Setenv(securityMasterKeyEnv, syntheticMasterKey)
	cipherText, err := Encrypt("synthetic-smtp-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(securityMasterKeyEnv, "abcdef0123456789abcdef0123456789")
	if plain, err := Decrypt(cipherText); err == nil || plain != "" {
		t.Fatal("wrong key must not yield plaintext")
	}
	t.Setenv(securityMasterKeyEnv, syntheticMasterKey)
	parts := strings.Split(cipherText, ":")
	first := byte('A')
	if parts[3][0] == first {
		first = 'B'
	}
	parts[3] = string(first) + parts[3][1:]
	if plain, err := Decrypt(strings.Join(parts, ":")); err == nil || plain != "" {
		t.Fatal("tampered ciphertext must not yield plaintext")
	}
}

func TestLegacyFallbackCiphertextIsNotAutomaticallyRecovered(t *testing.T) {
	for _, appEnv := range []string{"development", "production"} {
		t.Run(appEnv, func(t *testing.T) {
			t.Setenv("APP_ENV", appEnv)
			for _, key := range []string{"", syntheticMasterKey} {
				t.Setenv(securityMasterKeyEnv, key)
				if plain, err := Decrypt(legacySMTPFixture); err == nil || plain != "" {
					t.Fatal("legacy public-key ciphertext must require explicit administrative recovery")
				}
			}
		})
	}
	// Establish the provenance of the fixture using an explicitly supplied test key.
	t.Setenv(securityMasterKeyEnv, "development_fallback_key_32_bytes!!"[:32])
	if plain, err := Decrypt(legacySMTPFixture); err != nil || plain != "synthetic-legacy-smtp" {
		t.Fatalf("invalid historical test vector: %v", err)
	}
}

func TestDecryptEmptyAndLegacyPlaintextDoesNotNeedKey(t *testing.T) {
	t.Setenv(securityMasterKeyEnv, "")
	for _, input := range []string{"", "synthetic-legacy-plaintext"} {
		if got, err := Decrypt(input); err != nil || got != input {
			t.Fatalf("existing non-ciphertext compatibility changed: %v", err)
		}
	}
}
