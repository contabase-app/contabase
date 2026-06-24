package repository

import "database/sql"

type DashboardRepository struct {
	db *sql.DB
}

func NewDashboardRepository(db *sql.DB) *DashboardRepository {
	return &DashboardRepository{db: db}
}

func (r *DashboardRepository) FirstWorkspaceUser(workspaceID string) (string, string, error) {
	var userID, name string
	err := r.db.QueryRow(`
		SELECT u.id, u.name
		FROM users u
		JOIN workspace_members wm ON wm.user_id = u.id
		WHERE wm.workspace_id = ?
		ORDER BY wm.joined_at ASC
		LIMIT 1
	`, workspaceID).Scan(&userID, &name)
	return userID, name, err
}
