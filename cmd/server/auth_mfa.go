package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/httpcookies"

	"github.com/google/uuid"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

const (
	preAuthCookieName  = httpcookies.PreAuth
	backupCodeCount    = 8
	backupCodeByteSize = 5
)

type backupCodeHash struct {
	Hash string `json:"hash"`
}

var (
	backupCodeEncoding    = base32.StdEncoding.WithPadding(base32.NoPadding)
	devAuthKeyWarningOnce sync.Once
)

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func getAuthEncryptionKey() ([]byte, error) {
	keyB64 := strings.TrimSpace(os.Getenv("AUTH_ENCRYPTION_KEY"))
	if keyB64 != "" {
		key, err := base64.StdEncoding.DecodeString(keyB64)
		if err != nil {
			return nil, fmt.Errorf("invalid AUTH_ENCRYPTION_KEY base64: %w", err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("AUTH_ENCRYPTION_KEY must decode to 32 bytes")
		}
		return key, nil
	}

	appEnv := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	if appEnv == "" || appEnv == "development" {
		devAuthKeyWarningOnce.Do(func() {
			log.Println("⚠️ Aviso: AUTH_ENCRYPTION_KEY não definida. Usando chave padrão de desenvolvimento.")
		})
		return []byte("dev_secret_key_32_bytes_fallback"), nil
	}

	return nil, errors.New("AUTH_ENCRYPTION_KEY not configured")
}

func encryptTextForAuth(plain string) (string, error) {
	key, err := getAuthEncryptionKey()
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
	ciphertext := gcm.Seal(nil, nonce, []byte(plain), nil)
	return "v1:" + base64.RawURLEncoding.EncodeToString(nonce) + ":" + base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

func decryptTextForAuth(payload string) (string, error) {
	parts := strings.Split(strings.TrimSpace(payload), ":")
	if len(parts) != 3 || parts[0] != "v1" {
		return "", errors.New("invalid encrypted payload")
	}
	key, err := getAuthEncryptionKey()
	if err != nil {
		return "", err
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(parts[2])
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
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func generateBackupCodes() ([]string, error) {
	codes := make([]string, 0, backupCodeCount)
	for i := 0; i < backupCodeCount; i++ {
		buf := make([]byte, backupCodeByteSize)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		codes = append(codes, backupCodeEncoding.EncodeToString(buf))
	}
	return codes, nil
}

func normalizeBackupCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func hashBackupCodes(codes []string) ([]backupCodeHash, error) {
	out := make([]backupCodeHash, 0, len(codes))
	for _, code := range codes {
		h, err := bcrypt.GenerateFromPassword([]byte(normalizeBackupCode(code)), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		out = append(out, backupCodeHash{Hash: string(h)})
	}
	return out, nil
}

func marshalBackupCodeHashes(items []backupCodeHash) (string, error) {
	b, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalBackupCodeHashes(payload string) ([]backupCodeHash, error) {
	if strings.TrimSpace(payload) == "" {
		return []backupCodeHash{}, nil
	}
	var items []backupCodeHash
	if err := json.Unmarshal([]byte(payload), &items); err != nil {
		return nil, err
	}
	return items, nil
}

func consumeBackupCode(items []backupCodeHash, rawCode string) (bool, []backupCodeHash) {
	candidate := normalizeBackupCode(rawCode)
	if candidate == "" {
		return false, items
	}
	for i, item := range items {
		if bcrypt.CompareHashAndPassword([]byte(item.Hash), []byte(candidate)) == nil {
			next := make([]backupCodeHash, 0, len(items)-1)
			next = append(next, items[:i]...)
			next = append(next, items[i+1:]...)
			return true, next
		}
	}
	return false, items
}

func generateTOTPSetup(email string) (*otp.Key, string, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "ContaBase",
		AccountName: email,
		Period:      30,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return nil, "", err
	}
	img, err := key.Image(240, 240)
	if err != nil {
		return nil, "", err
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		return nil, "", err
	}
	qrCode := strings.TrimSpace(base64.StdEncoding.EncodeToString(pngBuf.Bytes()))
	return key, qrCode, nil
}

func validateTOTPCode(secret, code string, now time.Time) bool {
	ok, err := totp.ValidateCustom(strings.TrimSpace(code), strings.TrimSpace(secret), now, totp.ValidateOpts{
		Period:    30,
		Skew:      1,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	return err == nil && ok
}

func writeAuthAuditEvent(db *sql.DB, r *http.Request, userID, workspaceID, eventType string, metadata map[string]string) {
	metaBytes, _ := json.Marshal(metadata)
	_, err := db.Exec(`
		INSERT INTO auth_audit_events (id, user_id, workspace_id, event_type, ip, user_agent, metadata_json, created_at)
		VALUES (?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, unixepoch())
	`, uuid.NewString(), userID, workspaceID, eventType, clientIP(r), strings.TrimSpace(r.UserAgent()), string(metaBytes))
	if err != nil {
		log.Printf("auth audit insert failed: %v", err)
	}
}

func setPreAuthCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     preAuthCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   shouldUseSecureCookie(r),
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
	})
}

func clearPreAuthCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     preAuthCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   shouldUseSecureCookie(r),
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

func issueFinalSession(w http.ResponseWriter, r *http.Request, authService *auth.Service, userID string, isRemember bool) error {
	workspaceID, _, err := authService.ResolveWorkspaceMembership(userID)
	if err != nil {
		return err
	}
	ttl := 24 * time.Hour
	if isRemember {
		ttl = 30 * 24 * time.Hour
	}
	token, expiresAt, err := authService.CreateSession(userID, workspaceID, ttl, isRemember)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   shouldUseSecureCookie(r),
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
	})
	return nil
}
