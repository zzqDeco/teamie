package pg

import (
	"context"

	"teamie/pkg/postgres"
	"teamie/services/chat/internal/domain"
)

const marketplaceAppColumns = "id,slug,name,description,web_url,mcp_url,signing_secret,enabled,created_at,updated_at"

func scanMarketplaceApp(row rowScanner) (domain.MarketplaceApp, error) {
	var a domain.MarketplaceApp
	err := row.Scan(
		&a.ID,
		&a.Slug,
		&a.Name,
		&a.Description,
		&a.WebURL,
		&a.MCPURL,
		&a.SigningSecret,
		&a.Enabled,
		&a.CreatedAt,
		&a.UpdatedAt,
	)
	return a, err
}

func (r *Repository) ListAllMarketplaceApps(ctx context.Context) ([]domain.MarketplaceApp, error) {
	rows, err := r.DB.Conn(ctx).Query(ctx, "SELECT "+marketplaceAppColumns+" FROM marketplace_apps ORDER BY created_at,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.MarketplaceApp{}
	for rows.Next() {
		a, scanErr := scanMarketplaceApp(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) ListEnabledMarketplaceApps(ctx context.Context) ([]domain.MarketplaceApp, error) {
	rows, err := r.DB.Conn(ctx).Query(ctx, "SELECT "+marketplaceAppColumns+" FROM marketplace_apps WHERE enabled=true ORDER BY name,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.MarketplaceApp{}
	for rows.Next() {
		a, scanErr := scanMarketplaceApp(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) GetMarketplaceAppByID(ctx context.Context, id string, forUpdate bool) (domain.MarketplaceApp, error) {
	clause := ""
	if forUpdate {
		clause = " FOR UPDATE"
	}
	row := r.DB.Conn(ctx).QueryRow(ctx, "SELECT "+marketplaceAppColumns+" FROM marketplace_apps WHERE id=$1"+clause, id)
	a, err := scanMarketplaceApp(row)
	if postgres.IsNoRows(err) {
		return a, domain.ErrMarketplaceAppUnavailable()
	}
	return a, err
}

func (r *Repository) InsertMarketplaceApp(ctx context.Context, app domain.MarketplaceApp) (domain.MarketplaceApp, error) {
	row := r.DB.Conn(ctx).QueryRow(
		ctx,
		"INSERT INTO marketplace_apps(id,slug,name,description,web_url,mcp_url,signing_secret,enabled) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING "+marketplaceAppColumns,
		app.ID,
		app.Slug,
		app.Name,
		app.Description,
		app.WebURL,
		app.MCPURL,
		app.SigningSecret,
		app.Enabled,
	)
	created, err := scanMarketplaceApp(row)
	if postgres.IsUniqueViolation(err) {
		return created, domain.ErrDuplicateMarketplaceSlug()
	}
	return created, err
}

func (r *Repository) UpdateMarketplaceApp(ctx context.Context, app domain.MarketplaceApp) (domain.MarketplaceApp, error) {
	row := r.DB.Conn(ctx).QueryRow(
		ctx,
		"UPDATE marketplace_apps SET slug=$2,name=$3,description=$4,web_url=$5,mcp_url=$6,signing_secret=$7,enabled=$8,updated_at=now() WHERE id=$1 RETURNING "+marketplaceAppColumns,
		app.ID,
		app.Slug,
		app.Name,
		app.Description,
		app.WebURL,
		app.MCPURL,
		app.SigningSecret,
		app.Enabled,
	)
	updated, err := scanMarketplaceApp(row)
	if postgres.IsNoRows(err) {
		return updated, domain.ErrMarketplaceAppUnavailable()
	}
	if postgres.IsUniqueViolation(err) {
		return updated, domain.ErrDuplicateMarketplaceSlug()
	}
	return updated, err
}

func (r *Repository) DeleteMarketplaceApp(ctx context.Context, id string) (bool, error) {
	tag, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM marketplace_apps WHERE id=$1", id)
	return tag.RowsAffected() > 0, err
}

func (r *Repository) ListWorkspaceMarketplaceApps(ctx context.Context, workspaceID string) ([]domain.MarketplaceApp, error) {
	rows, err := r.DB.Conn(ctx).Query(
		ctx,
		"SELECT m.id,m.slug,m.name,m.description,m.web_url,m.mcp_url,m.signing_secret,m.enabled,m.created_at,m.updated_at FROM workspace_marketplace_apps w JOIN marketplace_apps m ON m.id=w.app_id WHERE w.workspace_id=$1 AND m.enabled=true ORDER BY m.name,m.id",
		workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.MarketplaceApp{}
	for rows.Next() {
		a, scanErr := scanMarketplaceApp(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) InstallWorkspaceMarketplaceApp(ctx context.Context, workspaceID, appID, installedBy string) error {
	_, err := r.DB.Conn(ctx).Exec(
		ctx,
		"INSERT INTO workspace_marketplace_apps(workspace_id,app_id,installed_by) VALUES($1,$2,$3) ON CONFLICT(workspace_id,app_id) DO NOTHING",
		workspaceID,
		appID,
		installedBy,
	)
	return err
}

func (r *Repository) UninstallWorkspaceMarketplaceApp(ctx context.Context, workspaceID, appID string) error {
	_, err := r.DB.Conn(ctx).Exec(
		ctx,
		"DELETE FROM workspace_marketplace_apps WHERE workspace_id=$1 AND app_id=$2",
		workspaceID,
		appID,
	)
	return err
}

func (r *Repository) GetInstalledWorkspaceMarketplaceApp(ctx context.Context, workspaceID, appID string) (domain.MarketplaceApp, error) {
	row := r.DB.Conn(ctx).QueryRow(
		ctx,
		"SELECT m.id,m.slug,m.name,m.description,m.web_url,m.mcp_url,m.signing_secret,m.enabled,m.created_at,m.updated_at FROM workspace_marketplace_apps w JOIN marketplace_apps m ON m.id=w.app_id WHERE w.workspace_id=$1 AND w.app_id=$2 AND m.enabled=true",
		workspaceID,
		appID,
	)
	a, err := scanMarketplaceApp(row)
	if postgres.IsNoRows(err) {
		return a, domain.ErrMarketplaceAppUnavailable()
	}
	return a, err
}
