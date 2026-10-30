package pg

import (
	"context"
	"encoding/json"

	"teamie/pkg/postgres"
	"teamie/services/chat/internal/domain"
)

// marketplaceAppSafeColumns deliberately omits signing_secret so list and detail
// queries never load symmetric secrets into memory.
const marketplaceAppSafeColumns = `id,slug,name,description,web_url,mcp_url,enabled,created_at,updated_at,` +
	`owner_id,is_published,published_version,review_status,` +
	`draft_name,draft_description,draft_web_url,draft_mcp_url,has_draft_changes,` +
	`submission_revision,submission_note,submitted_at,reviewed_by,reviewed_at,review_note,` +
	`admin_suspended,suspended_reason,suspended_at,last_verified_at,verification_report`

const marketplaceAppPrefixedSafeColumns = `m.id,m.slug,m.name,m.description,m.web_url,m.mcp_url,m.enabled,m.created_at,m.updated_at,` +
	`m.owner_id,m.is_published,m.published_version,m.review_status,` +
	`m.draft_name,m.draft_description,m.draft_web_url,m.draft_mcp_url,m.has_draft_changes,` +
	`m.submission_revision,m.submission_note,m.submitted_at,m.reviewed_by,m.reviewed_at,m.review_note,` +
	`m.admin_suspended,m.suspended_reason,m.suspended_at,m.last_verified_at,m.verification_report`

func scanMarketplaceAppSafe(row rowScanner) (domain.MarketplaceApp, error) {
	var a domain.MarketplaceApp
	var rawReport []byte
	err := row.Scan(
		&a.ID,
		&a.Slug,
		&a.Name,
		&a.Description,
		&a.WebURL,
		&a.MCPURL,
		&a.Enabled,
		&a.CreatedAt,
		&a.UpdatedAt,
		&a.OwnerID,
		&a.IsPublished,
		&a.PublishedVersion,
		&a.ReviewStatus,
		&a.DraftName,
		&a.DraftDescription,
		&a.DraftWebURL,
		&a.DraftMCPURL,
		&a.HasDraftChanges,
		&a.SubmissionRevision,
		&a.SubmissionNote,
		&a.SubmittedAt,
		&a.ReviewedBy,
		&a.ReviewedAt,
		&a.ReviewNote,
		&a.AdminSuspended,
		&a.SuspendedReason,
		&a.SuspendedAt,
		&a.LastVerifiedAt,
		&rawReport,
	)
	if err != nil {
		return a, err
	}
	if len(rawReport) > 0 {
		var parsed domain.MarketplaceEndpointVerification
		if jsonErr := json.Unmarshal(rawReport, &parsed); jsonErr == nil {
			a.VerificationReport = &parsed
		}
	}
	return a, nil
}

func marshalVerificationNullable(report *domain.MarketplaceEndpointVerification) ([]byte, error) {
	if report == nil {
		return nil, nil
	}
	return json.Marshal(report)
}

func (r *Repository) ListAllMarketplaceApps(ctx context.Context) ([]domain.MarketplaceApp, error) {
	rows, err := r.DB.Conn(ctx).Query(
		ctx,
		`SELECT `+marketplaceAppSafeColumns+` FROM marketplace_apps `+
			`ORDER BY CASE WHEN review_status='pending_review' THEN 0 ELSE 1 END, COALESCE(submitted_at, created_at) ASC, id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.MarketplaceApp{}
	for rows.Next() {
		a, scanErr := scanMarketplaceAppSafe(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) ListPublishedCatalogApps(ctx context.Context) ([]domain.MarketplaceApp, error) {
	rows, err := r.DB.Conn(ctx).Query(
		ctx,
		`SELECT `+marketplaceAppSafeColumns+` FROM marketplace_apps `+
			`WHERE is_published=true AND enabled=true AND admin_suspended=false ORDER BY name,id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.MarketplaceApp{}
	for rows.Next() {
		a, scanErr := scanMarketplaceAppSafe(rows)
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
	row := r.DB.Conn(ctx).QueryRow(ctx, "SELECT "+marketplaceAppSafeColumns+" FROM marketplace_apps WHERE id=$1"+clause, id)
	a, err := scanMarketplaceAppSafe(row)
	if postgres.IsNoRows(err) {
		return a, domain.ErrMarketplaceAppUnavailable()
	}
	return a, err
}

func (r *Repository) GetMarketplaceAppSigningSecret(ctx context.Context, id string) (string, error) {
	var secret string
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT signing_secret FROM marketplace_apps WHERE id=$1", id).Scan(&secret)
	if postgres.IsNoRows(err) {
		return "", domain.ErrMarketplaceAppUnavailable()
	}
	return secret, err
}

func (r *Repository) InsertMarketplaceApp(ctx context.Context, app domain.MarketplaceApp) (domain.MarketplaceApp, error) {
	rawReport, err := marshalVerificationNullable(app.VerificationReport)
	if err != nil {
		return domain.MarketplaceApp{}, err
	}
	row := r.DB.Conn(ctx).QueryRow(
		ctx,
		`INSERT INTO marketplace_apps(`+
			`id,slug,name,description,web_url,mcp_url,signing_secret,enabled,`+
			`owner_id,is_published,published_version,review_status,`+
			`draft_name,draft_description,draft_web_url,draft_mcp_url,has_draft_changes,`+
			`submission_revision,submission_note,submitted_at,reviewed_by,reviewed_at,review_note,`+
			`admin_suspended,suspended_reason,suspended_at,last_verified_at,verification_report`+
			`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28) `+
			`RETURNING `+marketplaceAppSafeColumns,
		app.ID, app.Slug, app.Name, app.Description, app.WebURL, app.MCPURL, app.SigningSecret, app.Enabled,
		app.OwnerID, app.IsPublished, app.PublishedVersion, app.ReviewStatus,
		app.DraftName, app.DraftDescription, app.DraftWebURL, app.DraftMCPURL, app.HasDraftChanges,
		app.SubmissionRevision, app.SubmissionNote, app.SubmittedAt, app.ReviewedBy, app.ReviewedAt, app.ReviewNote,
		app.AdminSuspended, app.SuspendedReason, app.SuspendedAt, app.LastVerifiedAt, rawReport,
	)
	created, scanErr := scanMarketplaceAppSafe(row)
	if postgres.IsUniqueViolation(scanErr) {
		return created, domain.ErrDuplicateMarketplaceSlug()
	}
	return created, scanErr
}

func (r *Repository) UpdateMarketplaceApp(ctx context.Context, app domain.MarketplaceApp) (domain.MarketplaceApp, error) {
	rawReport, err := marshalVerificationNullable(app.VerificationReport)
	if err != nil {
		return domain.MarketplaceApp{}, err
	}
	row := r.DB.Conn(ctx).QueryRow(
		ctx,
		`UPDATE marketplace_apps SET `+
			`slug=$2,name=$3,description=$4,web_url=$5,mcp_url=$6,enabled=$7,`+
			`is_published=$8,published_version=$9,review_status=$10,`+
			`draft_name=$11,draft_description=$12,draft_web_url=$13,draft_mcp_url=$14,has_draft_changes=$15,`+
			`submission_revision=$16,submission_note=$17,submitted_at=$18,reviewed_by=$19,reviewed_at=$20,review_note=$21,`+
			`admin_suspended=$22,suspended_reason=$23,suspended_at=$24,last_verified_at=$25,verification_report=$26,`+
			`updated_at=now() WHERE id=$1 RETURNING `+marketplaceAppSafeColumns,
		app.ID, app.Slug, app.Name, app.Description, app.WebURL, app.MCPURL, app.Enabled,
		app.IsPublished, app.PublishedVersion, app.ReviewStatus,
		app.DraftName, app.DraftDescription, app.DraftWebURL, app.DraftMCPURL, app.HasDraftChanges,
		app.SubmissionRevision, app.SubmissionNote, app.SubmittedAt, app.ReviewedBy, app.ReviewedAt, app.ReviewNote,
		app.AdminSuspended, app.SuspendedReason, app.SuspendedAt, app.LastVerifiedAt, rawReport,
	)
	updated, scanErr := scanMarketplaceAppSafe(row)
	if postgres.IsNoRows(scanErr) {
		return updated, domain.ErrMarketplaceAppUnavailable()
	}
	if postgres.IsUniqueViolation(scanErr) {
		return updated, domain.ErrDuplicateMarketplaceSlug()
	}
	return updated, scanErr
}

func (r *Repository) UpdateMarketplaceSigningSecret(ctx context.Context, id, secret string) (domain.MarketplaceApp, error) {
	row := r.DB.Conn(ctx).QueryRow(
		ctx,
		"UPDATE marketplace_apps SET signing_secret=$2,updated_at=now() WHERE id=$1 RETURNING "+marketplaceAppSafeColumns,
		id, secret,
	)
	updated, err := scanMarketplaceAppSafe(row)
	if postgres.IsNoRows(err) {
		return updated, domain.ErrMarketplaceAppUnavailable()
	}
	return updated, err
}

func (r *Repository) DeleteMarketplaceApp(ctx context.Context, id string) (bool, error) {
	tag, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM marketplace_apps WHERE id=$1", id)
	return tag.RowsAffected() > 0, err
}

func (r *Repository) ListWorkspaceMarketplaceApps(ctx context.Context, workspaceID string) ([]domain.InstalledWorkspaceMarketplaceApp, error) {
	rows, err := r.DB.Conn(ctx).Query(
		ctx,
		`SELECT `+marketplaceAppPrefixedSafeColumns+`,w.review_preview,ws.created_by `+
			`FROM workspace_marketplace_apps w `+
			`JOIN workspaces ws ON ws.id=w.workspace_id `+
			`JOIN marketplace_apps m ON m.id=w.app_id `+
			`WHERE w.workspace_id=$1 AND m.enabled=true AND m.admin_suspended=false `+
			`ORDER BY m.name,m.id`,
		workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.InstalledWorkspaceMarketplaceApp{}
	for rows.Next() {
		var item domain.InstalledWorkspaceMarketplaceApp
		var rawReport []byte
		if scanErr := rows.Scan(
			&item.App.ID, &item.App.Slug, &item.App.Name, &item.App.Description, &item.App.WebURL, &item.App.MCPURL, &item.App.Enabled, &item.App.CreatedAt, &item.App.UpdatedAt,
			&item.App.OwnerID, &item.App.IsPublished, &item.App.PublishedVersion, &item.App.ReviewStatus,
			&item.App.DraftName, &item.App.DraftDescription, &item.App.DraftWebURL, &item.App.DraftMCPURL, &item.App.HasDraftChanges,
			&item.App.SubmissionRevision, &item.App.SubmissionNote, &item.App.SubmittedAt, &item.App.ReviewedBy, &item.App.ReviewedAt, &item.App.ReviewNote,
			&item.App.AdminSuspended, &item.App.SuspendedReason, &item.App.SuspendedAt, &item.App.LastVerifiedAt, &rawReport,
			&item.ReviewPreview, &item.WorkspaceOwnerID,
		); scanErr != nil {
			return nil, scanErr
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Repository) InstallWorkspaceMarketplaceApp(ctx context.Context, workspaceID, appID, installedBy string, reviewPreview bool) error {
	_, err := r.DB.Conn(ctx).Exec(
		ctx,
		`INSERT INTO workspace_marketplace_apps(workspace_id,app_id,installed_by,review_preview) `+
			`VALUES($1,$2,$3,$4) ON CONFLICT(workspace_id,app_id) DO UPDATE SET review_preview=EXCLUDED.review_preview`,
		workspaceID, appID, installedBy, reviewPreview,
	)
	return err
}

func (r *Repository) UninstallWorkspaceMarketplaceApp(ctx context.Context, workspaceID, appID string) error {
	_, err := r.DB.Conn(ctx).Exec(
		ctx,
		"DELETE FROM workspace_marketplace_apps WHERE workspace_id=$1 AND app_id=$2",
		workspaceID, appID,
	)
	return err
}

func (r *Repository) GetInstalledWorkspaceMarketplaceAppForSession(ctx context.Context, workspaceID, appID string) (domain.InstalledWorkspaceMarketplaceApp, error) {
	var item domain.InstalledWorkspaceMarketplaceApp
	var rawReport []byte
	err := r.DB.Conn(ctx).QueryRow(
		ctx,
		`SELECT `+marketplaceAppPrefixedSafeColumns+`,m.signing_secret,w.review_preview,ws.created_by `+
			`FROM workspace_marketplace_apps w `+
			`JOIN workspaces ws ON ws.id=w.workspace_id `+
			`JOIN marketplace_apps m ON m.id=w.app_id `+
			`WHERE w.workspace_id=$1 AND w.app_id=$2 AND m.enabled=true AND m.admin_suspended=false`,
		workspaceID, appID,
	).Scan(
		&item.App.ID, &item.App.Slug, &item.App.Name, &item.App.Description, &item.App.WebURL, &item.App.MCPURL, &item.App.Enabled, &item.App.CreatedAt, &item.App.UpdatedAt,
		&item.App.OwnerID, &item.App.IsPublished, &item.App.PublishedVersion, &item.App.ReviewStatus,
		&item.App.DraftName, &item.App.DraftDescription, &item.App.DraftWebURL, &item.App.DraftMCPURL, &item.App.HasDraftChanges,
		&item.App.SubmissionRevision, &item.App.SubmissionNote, &item.App.SubmittedAt, &item.App.ReviewedBy, &item.App.ReviewedAt, &item.App.ReviewNote,
		&item.App.AdminSuspended, &item.App.SuspendedReason, &item.App.SuspendedAt, &item.App.LastVerifiedAt, &rawReport,
		&item.App.SigningSecret, &item.ReviewPreview, &item.WorkspaceOwnerID,
	)
	if postgres.IsNoRows(err) {
		return item, domain.ErrMarketplaceAppUnavailable()
	}
	return item, err
}
