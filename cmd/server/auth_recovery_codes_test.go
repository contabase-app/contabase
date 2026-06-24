package main

import (
	"database/sql"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

const testTOTPSecret = "JBSWY3DPEHPK3PXP"

var canonicalBackupCodePattern = regexp.MustCompile(`^[A-Z2-7]{8}$`)

type recoveryCodesTemplateEngine struct {
	name string
	data any
}

func (e *recoveryCodesTemplateEngine) ExecuteTemplate(_ io.Writer, name string, data any) error {
	e.name = name
	e.data = data
	return nil
}

func (e *recoveryCodesTemplateEngine) Lookup(string) *template.Template {
	return nil
}

func TestGeneratedBackupCodesUseCanonicalFormatAndMatchHashes(t *testing.T) {
	codes, err := generateBackupCodes()
	if err != nil {
		t.Fatalf("generate backup codes: %v", err)
	}
	if len(codes) != 8 {
		t.Fatalf("generated %d backup codes, want 8", len(codes))
	}

	hashes, err := hashBackupCodes(codes)
	if err != nil {
		t.Fatalf("hash backup codes: %v", err)
	}
	if len(hashes) != len(codes) {
		t.Fatalf("generated %d hashes for %d codes", len(hashes), len(codes))
	}

	for i, code := range codes {
		if !canonicalBackupCodePattern.MatchString(code) {
			t.Fatalf("backup code %q does not match canonical 8-character format", code)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hashes[i].Hash), []byte(code)); err != nil {
			t.Fatalf("displayed backup code %q does not match persisted hash: %v", code, err)
		}
	}
}

func TestTOTPActivationPersistsDisplayedRecoveryCodesAndLoginConsumesOnce(t *testing.T) {
	t.Setenv("AUTH_ENCRYPTION_KEY", testAuthEncryptionKey("recovery"))
	db, authService, userID := setupTestDB(t)
	defer db.Close()

	code, err := totp.GenerateCode(testTOTPSecret, time.Now())
	if err != nil {
		t.Fatalf("generate TOTP code: %v", err)
	}
	activationForm := url.Values{
		"totp_secret": {testTOTPSecret},
		"code":        {code},
	}
	activationReq := httptest.NewRequest(http.MethodPost, "/configuracoes/seguranca/totp/confirmar", strings.NewReader(activationForm.Encode()))
	activationReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	activationRR := httptest.NewRecorder()
	activationTpl := &recoveryCodesTemplateEngine{}

	handleTOTPSetupConfirm(activationRR, activationReq, activationTpl, authService, db, userID)

	if activationRR.Code != http.StatusOK {
		t.Fatalf("activation status = %d, want %d", activationRR.Code, http.StatusOK)
	}
	if activationTpl.name != "perfil-2fa-enabled" {
		t.Fatalf("activation template = %q, want perfil-2fa-enabled", activationTpl.name)
	}
	activationData, ok := activationTpl.data.(securityPageData)
	if !ok {
		t.Fatalf("activation data type = %T, want securityPageData", activationTpl.data)
	}
	if len(activationData.BackupCodes) != 8 {
		t.Fatalf("displayed %d backup codes, want 8", len(activationData.BackupCodes))
	}

	payload, err := authService.GetBackupCodeHashes(userID)
	if err != nil {
		t.Fatalf("read persisted backup code hashes: %v", err)
	}
	hashes, err := unmarshalBackupCodeHashes(payload)
	if err != nil {
		t.Fatalf("decode persisted backup code hashes: %v", err)
	}
	for i, displayedCode := range activationData.BackupCodes {
		if !canonicalBackupCodePattern.MatchString(displayedCode) {
			t.Fatalf("displayed backup code %q does not match canonical format", displayedCode)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hashes[i].Hash), []byte(displayedCode)); err != nil {
			t.Fatalf("displayed backup code %q does not match persisted hash: %v", displayedCode, err)
		}
	}

	usedCode := activationData.BackupCodes[0]
	assertRecoveryCodeLoginStatus(t, db, authService, userID, usedCode, http.StatusSeeOther)

	payload, err = authService.GetBackupCodeHashes(userID)
	if err != nil {
		t.Fatalf("read hashes after recovery login: %v", err)
	}
	hashes, err = unmarshalBackupCodeHashes(payload)
	if err != nil {
		t.Fatalf("decode hashes after recovery login: %v", err)
	}
	if len(hashes) != 7 {
		t.Fatalf("remaining backup codes = %d, want 7", len(hashes))
	}

	assertRecoveryCodeLoginStatus(t, db, authService, userID, usedCode, http.StatusUnauthorized)
	assertRecoveryCodeLoginStatus(t, db, authService, userID, "INVALID1", http.StatusUnauthorized)
}

func TestTOTPLoginStillAcceptsAuthenticatorCode(t *testing.T) {
	t.Setenv("AUTH_ENCRYPTION_KEY", testAuthEncryptionKey("totp-regression"))
	db, authService, userID := setupTestDB(t)
	defer db.Close()

	encSecret, err := encryptTextForAuth(testTOTPSecret)
	if err != nil {
		t.Fatalf("encrypt TOTP secret: %v", err)
	}
	if err := authService.UpdateTOTPSetup(userID, encSecret, "[]", true); err != nil {
		t.Fatalf("enable TOTP: %v", err)
	}
	code, err := totp.GenerateCode(testTOTPSecret, time.Now())
	if err != nil {
		t.Fatalf("generate TOTP code: %v", err)
	}

	preAuthCookie := createPreAuthCookieForRecoveryTest(t, authService, userID)
	form := url.Values{"code": {code}}
	req := httptest.NewRequest(http.MethodPost, "/login/2fa", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(preAuthCookie)
	rr := httptest.NewRecorder()

	handleLogin2FASubmit(rr, req, dummyTemplateEngine{}, authService, db, "csrf")

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("TOTP login status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
}

func TestLegacySevenCharacterRecoveryCodeRemainsUsable(t *testing.T) {
	t.Setenv("AUTH_ENCRYPTION_KEY", testAuthEncryptionKey("legacy-recovery"))
	db, authService, userID := setupTestDB(t)
	defer db.Close()

	legacyCode := "ABCD-12"
	hashes, err := hashBackupCodes([]string{legacyCode})
	if err != nil {
		t.Fatalf("hash legacy backup code: %v", err)
	}
	payload, err := marshalBackupCodeHashes(hashes)
	if err != nil {
		t.Fatalf("marshal legacy backup code hash: %v", err)
	}
	encSecret, err := encryptTextForAuth(testTOTPSecret)
	if err != nil {
		t.Fatalf("encrypt TOTP secret: %v", err)
	}
	if err := authService.UpdateTOTPSetup(userID, encSecret, payload, true); err != nil {
		t.Fatalf("enable TOTP with legacy recovery code: %v", err)
	}

	assertRecoveryCodeLoginStatus(t, db, authService, userID, legacyCode, http.StatusSeeOther)
}

func TestConcurrentRecoveryCodeLoginAuthenticatesOnlyOnce(t *testing.T) {
	t.Setenv("AUTH_ENCRYPTION_KEY", testAuthEncryptionKey("concurrent-recovery"))
	db, authService, userID := setupTestDB(t)
	defer db.Close()

	recoveryCode := "AB2CDE34"
	hashes, err := hashBackupCodes([]string{recoveryCode})
	if err != nil {
		t.Fatalf("hash recovery code: %v", err)
	}
	payload, err := marshalBackupCodeHashes(hashes)
	if err != nil {
		t.Fatalf("marshal recovery code hash: %v", err)
	}
	encSecret, err := encryptTextForAuth(testTOTPSecret)
	if err != nil {
		t.Fatalf("encrypt TOTP secret: %v", err)
	}
	if err := authService.UpdateTOTPSetup(userID, encSecret, payload, true); err != nil {
		t.Fatalf("enable TOTP with recovery code: %v", err)
	}

	cookies := []*http.Cookie{
		createPreAuthCookieForRecoveryTest(t, authService, userID),
		createPreAuthCookieForRecoveryTest(t, authService, userID),
	}
	start := make(chan struct{})
	statuses := make(chan int, len(cookies))
	var ready sync.WaitGroup
	ready.Add(len(cookies))
	var attempts sync.WaitGroup
	attempts.Add(len(cookies))

	for _, cookie := range cookies {
		go func(preAuthCookie *http.Cookie) {
			defer attempts.Done()
			ready.Done()
			<-start
			statuses <- submitRecoveryCodeLogin(db, authService, recoveryCode, preAuthCookie)
		}(cookie)
	}
	ready.Wait()
	close(start)
	attempts.Wait()
	close(statuses)

	successes := 0
	rejections := 0
	for status := range statuses {
		switch status {
		case http.StatusSeeOther:
			successes++
		case http.StatusUnauthorized:
			rejections++
		default:
			t.Fatalf("unexpected concurrent recovery code status = %d", status)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent recovery code successes = %d, want exactly 1", successes)
	}
	if rejections != 1 {
		t.Fatalf("concurrent recovery code rejections = %d, want exactly 1", rejections)
	}

	payload, err = authService.GetBackupCodeHashes(userID)
	if err != nil {
		t.Fatalf("read hashes after concurrent recovery login: %v", err)
	}
	remaining, err := unmarshalBackupCodeHashes(payload)
	if err != nil {
		t.Fatalf("decode hashes after concurrent recovery login: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining recovery code hashes = %d, want 0", len(remaining))
	}
}

func TestRecoveryCodeInputMatchesCanonicalFormat(t *testing.T) {
	templatePath := filepath.Join("..", "..", "templates", "pages", "login_2fa.html")
	content, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read login 2FA template: %v", err)
	}
	html := string(content)
	for _, expected := range []string{
		`name="backup_code"`,
		`minlength="7"`,
		`maxlength="8"`,
		`pattern="[A-Za-z0-9_-]{7,8}"`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("login 2FA template missing %q", expected)
		}
	}
}

func assertRecoveryCodeLoginStatus(
	t *testing.T,
	db *sql.DB,
	authService *auth.Service,
	userID, backupCode string,
	wantStatus int,
) {
	t.Helper()

	preAuthCookie := createPreAuthCookieForRecoveryTest(t, authService, userID)
	status := submitRecoveryCodeLogin(db, authService, backupCode, preAuthCookie)
	if status != wantStatus {
		t.Fatalf("recovery code login status = %d, want %d", status, wantStatus)
	}
}

func submitRecoveryCodeLogin(
	db *sql.DB,
	authService *auth.Service,
	backupCode string,
	preAuthCookie *http.Cookie,
) int {
	form := url.Values{"backup_code": {backupCode}}
	req := httptest.NewRequest(http.MethodPost, "/login/2fa", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(preAuthCookie)
	rr := httptest.NewRecorder()

	handleLogin2FASubmit(rr, req, dummyTemplateEngine{}, authService, db, "csrf")

	return rr.Code
}

func createPreAuthCookieForRecoveryTest(t *testing.T, authService *auth.Service, userID string) *http.Cookie {
	t.Helper()

	token, _, err := authService.CreatePreAuthSession(userID, "TOTP", 5*time.Minute, false)
	if err != nil {
		t.Fatalf("create pre-auth session: %v", err)
	}
	return &http.Cookie{Name: preAuthCookieName, Value: token}
}
