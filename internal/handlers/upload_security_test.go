package handlers

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/paths"
)

func TestSaveUploadedImageFileAcceptsPNGAndIgnoresOriginalName(t *testing.T) {
	req := newUploadRequest(t, "photo_file", "../../evil.png", testPNGBytes())
	uploadDir := t.TempDir()

	fileName, err := saveUploadedImageFile(req, "photo_file", uploadDir, "profile", profileImageMaxBytes)
	if err != nil {
		t.Fatalf("saveUploadedImageFile: %v", err)
	}
	if fileName == "" {
		t.Fatalf("expected saved file name")
	}
	if strings.Contains(fileName, "evil") || strings.Contains(fileName, "..") || filepath.Base(fileName) != fileName {
		t.Fatalf("unsafe generated file name: %q", fileName)
	}
	if filepath.Ext(fileName) != ".png" {
		t.Fatalf("expected .png extension, got %q", fileName)
	}
	if _, err := os.Stat(filepath.Join(uploadDir, fileName)); err != nil {
		t.Fatalf("saved file missing: %v", err)
	}
}

func TestSaveUploadedImageFileAcceptsWebP(t *testing.T) {
	req := newUploadRequest(t, "logo_light_file", "logo.webp", testWebPBytes())
	uploadDir := t.TempDir()

	fileName, err := saveUploadedImageFile(req, "logo_light_file", uploadDir, "logo-light", workspaceLogoMaxBytes)
	if err != nil {
		t.Fatalf("saveUploadedImageFile: %v", err)
	}
	if filepath.Ext(fileName) != ".webp" {
		t.Fatalf("expected .webp extension, got %q", fileName)
	}
}

func TestProfileUploadUsesConfiguredUploadsDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", filepath.Join(root, "state"))
	t.Setenv("UPLOADS_DIR", filepath.Join(root, "uploads-root"))

	req := newUploadRequest(t, "photo_file", "avatar.png", testPNGBytes())
	fileName, err := saveUploadedImageFile(req, "photo_file", paths.ProfileUploadsDir(), "profile", profileImageMaxBytes)
	if err != nil {
		t.Fatalf("saveUploadedImageFile: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "uploads-root", "profile", fileName)); err != nil {
		t.Fatalf("saved profile file missing in UPLOADS_DIR: %v", err)
	}
}

func TestWorkspaceLogoUsesConfiguredUploadsDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", filepath.Join(root, "state"))
	t.Setenv("UPLOADS_DIR", filepath.Join(root, "uploads-root"))

	req := newUploadRequest(t, "logo_light_file", "logo.png", testPNGBytes())
	publicPath, err := saveWorkspaceLogoFile(req, "logo_light_file", "workspace-1", "light")
	if err != nil {
		t.Fatalf("saveWorkspaceLogoFile: %v", err)
	}
	if !strings.HasPrefix(publicPath, "/uploads/workspaces/workspace-1/") {
		t.Fatalf("public path = %q", publicPath)
	}
	fileName := strings.TrimPrefix(publicPath, "/uploads/workspaces/workspace-1/")
	if _, err := os.Stat(filepath.Join(root, "uploads-root", "workspaces", "workspace-1", fileName)); err != nil {
		t.Fatalf("saved workspace logo missing in UPLOADS_DIR: %v", err)
	}
}

func TestSafeAttachmentFullPathUsesConfiguredUploadsDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", filepath.Join(root, "state"))
	t.Setenv("UPLOADS_DIR", filepath.Join(root, "uploads-root"))

	got, err := safeAttachmentFullPath("workspace-1", "uploads/workspace-1/receipt.pdf")
	if err != nil {
		t.Fatalf("safeAttachmentFullPath: %v", err)
	}
	want := filepath.Join(root, "uploads-root", "workspace-1", "receipt.pdf")
	if got != want {
		t.Fatalf("safeAttachmentFullPath() = %q, want %q", got, want)
	}
}

func TestSaveUploadedImageFileBlocksOversizedUpload(t *testing.T) {
	content := append(testPNGBytes(), bytes.Repeat([]byte{0}, 32)...)
	req := newUploadRequest(t, "photo_file", "avatar.png", content)

	_, err := saveUploadedImageFile(req, "photo_file", t.TempDir(), "profile", int64(len(testPNGBytes())))
	if err == nil || !strings.Contains(err.Error(), "arquivo muito grande") {
		t.Fatalf("expected file too large error, got %v", err)
	}
}

func TestSaveUploadedImageFileBlocksDangerousExtension(t *testing.T) {
	req := newUploadRequest(t, "photo_file", "avatar.html", testPNGBytes())

	_, err := saveUploadedImageFile(req, "photo_file", t.TempDir(), "profile", profileImageMaxBytes)
	if err == nil || !strings.Contains(err.Error(), "formato não permitido") {
		t.Fatalf("expected extension error, got %v", err)
	}
}

func TestSaveUploadedImageFileBlocksSVG(t *testing.T) {
	req := newUploadRequest(t, "logo_light_file", "logo.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`))

	_, err := saveUploadedImageFile(req, "logo_light_file", t.TempDir(), "logo-light", workspaceLogoMaxBytes)
	if err == nil || !strings.Contains(err.Error(), "SVG não é permitido") {
		t.Fatalf("expected svg blocked error, got %v", err)
	}
}

func TestSaveUploadedImageFileBlocksMIMEInconsistency(t *testing.T) {
	req := newUploadRequest(t, "photo_file", "avatar.png", []byte("<html></html>"))

	_, err := saveUploadedImageFile(req, "photo_file", t.TempDir(), "profile", profileImageMaxBytes)
	if err == nil || !strings.Contains(err.Error(), "inconsistente") {
		t.Fatalf("expected MIME inconsistency error, got %v", err)
	}
}

func newUploadRequest(t *testing.T, fieldName, fileName string, content []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(fieldName, fileName)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write file content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "/upload", &body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func testPNGBytes() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde,
	}
}

func testWebPBytes() []byte {
	return []byte{
		'R', 'I', 'F', 'F', 0x1a, 0x00, 0x00, 0x00,
		'W', 'E', 'B', 'P', 'V', 'P', '8', ' ',
		0x0e, 0x00, 0x00, 0x00, 0x2f, 0x00, 0x00, 0x00,
		0x10, 0x07, 0x10, 0x11, 0x11, 0x88, 0x88, 0xfe,
		0x07, 0x00,
	}
}
