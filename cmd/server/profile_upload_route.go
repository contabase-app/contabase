package main

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strings"

	"github.com/contabase-app/contabase/internal/auth"
)

func profileUploadRoute(authService *auth.Service, csrfSigner *csrfSigner) http.HandlerFunc {
	return withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		fileName := strings.TrimPrefix(r.URL.Path, "/uploads/profile/")
		if _, ok := uploadedImageContentType(fileName); !ok {
			http.NotFound(w, r)
			return
		}
		visible, err := profileUploadVisibleInWorkspace(authService.DB, "/uploads/profile/"+fileName, ctx.ActiveWorkspaceID)
		if err != nil {
			slog.Error("profile upload authorization failed", "workspace_id", ctx.ActiveWorkspaceID, "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if !visible {
			http.NotFound(w, r)
			return
		}
		serveProfileUpload(w, r)
	})
}

func profileUploadVisibleInWorkspace(db *sql.DB, profilePath, workspaceID string) (bool, error) {
	var visible bool
	err := db.QueryRow(`
		SELECT EXISTS (
			SELECT 1
			FROM users u
			JOIN workspace_members wm ON wm.user_id = u.id
			WHERE u.profile_photo_path = ?
			  AND wm.workspace_id = ?
			  AND COALESCE(u.deleted_at, 0) = 0
		)
	`, profilePath, workspaceID).Scan(&visible)
	return visible, err
}
