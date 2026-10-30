package pg

import (
	"context"
	"encoding/json"
	"time"

	"teamie/pkg/postgres"
	"teamie/services/marketplace/internal/domain"
)

// marketplaceAppSafeColumns deliberately omits signing_secret so list and detail
// queries never load symmetric secrets into memory.
const marketplaceAppSafeColumns = `id,slug,name,description,web_url,mcp_url,enabled,owner_id,is_published,published_version,review_status,draft_name,draft_description,draft_web_url,draft_mcp_url,has_draft_changes,submission_revision,submission_note,submitted_at,reviewed_by,reviewed_at,review_note,admin_suspended,suspended_reason,suspended_at,last_verified_at,verification_report,created_at,updated_at`

const marketplaceAppPrefixedSafeColumns = `a.id,a.slug,a.name,a.description,a.web_url,a.mcp_url,a.enabled,a.owner_id,a.is_published,a.published_version,a.review_status,a.draft_name,a.draft_description,a.draft_web_url,a.draft_mcp_url,a.has_draft_changes,a.submission_revision,a.submission_note,a.submitted_at,a.reviewed_by,a.reviewed_at,a.review_note,a.admin_suspended,a.suspended_reason,a.suspended_at,a.last_verified_at,a.verification_report,a.created_at,a.updated_at`

type rowScanner interface {
	Scan(dest ...any) error
}

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
		&a.CreatedAt,
		&a.UpdatedAt,
	)
	if err != nil {
		return domain.MarketplaceApp{}, err
	}
	if len(rawReport) > 0 && string(rawReport) != "null" {
		var rep domain.MarketplaceEndpointVerification
		if uerr := json.Unmarshal(rawReport, &rep); uerr == nil {
			a.VerificationReport = &rep
		}
	}
	return a, nil
}

func marshalReport(rep *domain.MarketplaceEndpointVerification) ([]byte, error) {
	if rep == nil {
		return nil, nil
	}
	return json.Marshal(rep)
}

func (r *Repository) ListAllMarketplaceApps(ctx context.Context) ([]domain.MarketplaceApp, error) {
	rows, err := r.DB.Conn(ctx).Query(
		ctx,
		`SELECT `+marketplaceAppSafeColumns+` FROM marketplace_apps ORDER BY CASE WHEN review_status='pending_review' THEN 0 ELSE 1 END, COALESCE(submitted_at, created_at) DESC, id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.MarketplaceApp{}
	for rows.Next() {
		app, scanErr := scanMarketplaceAppSafe(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, app)
	}
	return out, rows.Err()
}

func (r *Repository) ListPublishedCatalogApps(ctx context.Context) ([]domain.MarketplaceApp, error) {
	rows, err := r.DB.Conn(ctx).Query(
		ctx,
		`SELECT `+marketplaceAppSafeColumns+` FROM marketplace_apps WHERE enabled=true AND is_published=true AND admin_suspended=false ORDER BY CASE WHEN owner_id IS NULL THEN 0 ELSE 1 END, created_at ASC, id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.MarketplaceApp{}
	for rows.Next() {
		app, scanErr := scanMarketplaceAppSafe(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, app)
	}
	return out, rows.Err()
}

func (r *Repository) ListDeveloperMarketplaceApps(ctx context.Context, ownerID string) ([]domain.MarketplaceApp, error) {
	rows, err := r.DB.Conn(ctx).Query(
		ctx,
		`SELECT `+marketplaceAppSafeColumns+` FROM marketplace_apps WHERE owner_id=$1 ORDER BY created_at DESC, id DESC`,
		ownerID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.MarketplaceApp{}
	for rows.Next() {
		app, scanErr := scanMarketplaceAppSafe(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, app)
	}
	return out, rows.Err()
}

func (r *Repository) CountDeveloperMarketplaceApps(ctx context.Context, ownerID string) (int, error) {
	var count int
	err := r.DB.Conn(ctx).QueryRow(ctx, `SELECT count(*) FROM marketplace_apps WHERE owner_id=$1`, ownerID).Scan(&count)
	return count, err
}

func (r *Repository) GetMarketplaceAppByID(ctx context.Context, id string, forUpdate bool) (domain.MarketplaceApp, error) {
	query := `SELECT ` + marketplaceAppSafeColumns + ` FROM marketplace_apps WHERE id=$1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	app, err := scanMarketplaceAppSafe(r.DB.Conn(ctx).QueryRow(ctx, query, id))
	if err != nil {
		return domain.MarketplaceApp{}, notFound(err)
	}
	return app, nil
}

func (r *Repository) GetMarketplaceAppSigningSecret(ctx context.Context, id string) (string, error) {
	var secret string
	err := r.DB.Conn(ctx).QueryRow(ctx, `SELECT signing_secret FROM marketplace_apps WHERE id=$1`, id).Scan(&secret)
	if err != nil {
		return "", notFound(err)
	}
	return secret, nil
}

func (r *Repository) InsertMarketplaceApp(ctx context.Context, app domain.MarketplaceApp) (domain.MarketplaceApp, error) {
	reportJSON, err := marshalReport(app.VerificationReport)
	if err != nil {
		return domain.MarketplaceApp{}, err
	}
	row := r.DB.Conn(ctx).QueryRow(
		ctx,
		`INSERT INTO marketplace_apps (
			id,slug,name,description,web_url,mcp_url,signing_secret,enabled,owner_id,
			is_published,published_version,review_status,draft_name,draft_description,draft_web_url,draft_mcp_url,
			has_draft_changes,submission_revision,submission_note,submitted_at,reviewed_by,reviewed_at,review_note,
			admin_suspended,suspended_reason,suspended_at,last_verified_at,verification_report,created_at,updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,
			$10,$11,$12,$13,$14,$15,$16,
			$17,$18,$19,$20,$21,$22,$23,
			$24,$25,$26,$27,$28,$29,$30
		) RETURNING `+marketplaceAppSafeColumns,
		app.ID, app.Slug, app.Name, app.Description, app.WebURL, app.MCPURL, app.SigningSecret, app.Enabled, app.OwnerID,
		app.IsPublished, app.PublishedVersion, app.ReviewStatus, app.DraftName, app.DraftDescription, app.DraftWebURL, app.DraftMCPURL,
		app.HasDraftChanges, app.SubmissionRevision, app.SubmissionNote, app.SubmittedAt, app.ReviewedBy, app.ReviewedAt, app.ReviewNote,
		app.AdminSuspended, app.SuspendedReason, app.SuspendedAt, app.LastVerifiedAt, reportJSON, app.CreatedAt, app.UpdatedAt,
	)
	created, scanErr := scanMarketplaceAppSafe(row)
	if scanErr != nil {
		return domain.MarketplaceApp{}, mapWriteError(scanErr)
	}
	return created, nil
}

func (r *Repository) UpdateMarketplaceApp(ctx context.Context, app domain.MarketplaceApp) (domain.MarketplaceApp, error) {
	reportJSON, err := marshalReport(app.VerificationReport)
	if err != nil {
		return domain.MarketplaceApp{}, err
	}
	row := r.DB.Conn(ctx).QueryRow(
		ctx,
		`UPDATE marketplace_apps SET
			slug=$2,
			name=$3,
			description=$4,
			web_url=$5,
			mcp_url=$6,
			enabled=$7,
			is_published=$8,
			published_version=$9,
			review_status=$10,
			draft_name=$11,
			draft_description=$12,
			draft_web_url=$13,
			draft_mcp_url=$14,
			has_draft_changes=$15,
			submission_revision=$16,
			submission_note=$17,
			submitted_at=$18,
			reviewed_by=$19,
			reviewed_at=$20,
			review_note=$21,
			admin_suspended=$22,
			suspended_reason=$23,
			suspended_at=$24,
			last_verified_at=$25,
			verification_report=$26,
			updated_at=$27
		WHERE id=$1
		RETURNING `+marketplaceAppSafeColumns,
		app.ID, app.Slug, app.Name, app.Description, app.WebURL, app.MCPURL, app.Enabled,
		app.IsPublished, app.PublishedVersion, app.ReviewStatus, app.DraftName, app.DraftDescription, app.DraftWebURL, app.DraftMCPURL,
		app.HasDraftChanges, app.SubmissionRevision, app.SubmissionNote, app.SubmittedAt, app.ReviewedBy, app.ReviewedAt, app.ReviewNote,
		app.AdminSuspended, app.SuspendedReason, app.SuspendedAt, app.LastVerifiedAt, reportJSON, app.UpdatedAt,
	)
	updated, scanErr := scanMarketplaceAppSafe(row)
	if scanErr != nil {
		return domain.MarketplaceApp{}, mapWriteError(scanErr)
	}
	return updated, nil
}

func (r *Repository) UpdateMarketplaceSigningSecret(ctx context.Context, id, secret string) (domain.MarketplaceApp, error) {
	row := r.DB.Conn(ctx).QueryRow(
		ctx,
		`UPDATE marketplace_apps
		 SET signing_secret=$2, last_verified_at=NULL, verification_report=NULL, updated_at=now()
		 WHERE id=$1
		 RETURNING `+marketplaceAppSafeColumns,
		id,
		secret,
	)
	updated, err := scanMarketplaceAppSafe(row)
	if err != nil {
		return domain.MarketplaceApp{}, notFound(err)
	}
	return updated, nil
}

func (r *Repository) SaveMarketplaceVerificationReport(
	ctx context.Context,
	id string,
	verifiedAt time.Time,
	report domain.MarketplaceEndpointVerification,
	fence domain.VerificationSaveFence,
) (domain.MarketplaceApp, error) {
	reportJSON, err := marshalReport(&report)
	if err != nil {
		return domain.MarketplaceApp{}, err
	}
	row := r.DB.Conn(ctx).QueryRow(
		ctx,
		`UPDATE marketplace_apps
		 SET last_verified_at=$2, verification_report=$3, updated_at=now()
		 WHERE id=$1
		   AND slug=$4
		   AND draft_web_url=$5
		   AND draft_mcp_url=$6
		   AND web_url=$7
		   AND mcp_url=$8
		   AND signing_secret=$9
		   AND ($10::boolean OR admin_suspended=false)
		 RETURNING `+marketplaceAppSafeColumns,
		id,
		verifiedAt,
		reportJSON,
		fence.Slug,
		fence.DraftWebURL,
		fence.DraftMCPURL,
		fence.WebURL,
		fence.MCPURL,
		fence.SigningSecret,
		fence.IgnoreSuspended,
	)
	updated, scanErr := scanMarketplaceAppSafe(row)
	if scanErr == nil {
		return updated, nil
	}
	if !postgres.IsNoRows(scanErr) {
		return domain.MarketplaceApp{}, scanErr
	}
	var adminSuspended bool
	chkErr := r.DB.Conn(ctx).QueryRow(ctx, `SELECT admin_suspended FROM marketplace_apps WHERE id=$1`, id).Scan(&adminSuspended)
	if postgres.IsNoRows(chkErr) {
		return domain.MarketplaceApp{}, domain.ErrMarketplaceAppUnavailable()
	}
	if chkErr != nil {
		return domain.MarketplaceApp{}, chkErr
	}
	if !fence.IgnoreSuspended && adminSuspended {
		return domain.MarketplaceApp{}, domain.ErrMarketplaceSuspended()
	}
	return domain.MarketplaceApp{}, domain.ErrMarketplaceRevisionConflict()
}

func (r *Repository) DeleteMarketplaceApp(ctx context.Context, id string) (bool, error) {
	tag, err := r.DB.Conn(ctx).Exec(ctx, `DELETE FROM marketplace_apps WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (r *Repository) ListWorkspaceMarketplaceApps(ctx context.Context, workspaceID string) ([]domain.InstalledWorkspaceMarketplaceApp, error) {
	rows, err := r.DB.Conn(ctx).Query(
		ctx,
		`SELECT `+marketplaceAppPrefixedSafeColumns+`, w.review_preview
		 FROM workspace_marketplace_apps w
		 JOIN marketplace_apps a ON a.id=w.app_id
		 WHERE w.workspace_id=$1
		 ORDER BY w.created_at ASC, a.id ASC`,
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
		if err := rows.Scan(
			&item.App.ID,
			&item.App.Slug,
			&item.App.Name,
			&item.App.Description,
			&item.App.WebURL,
			&item.App.MCPURL,
			&item.App.Enabled,
			&item.App.OwnerID,
			&item.App.IsPublished,
			&item.App.PublishedVersion,
			&item.App.ReviewStatus,
			&item.App.DraftName,
			&item.App.DraftDescription,
			&item.App.DraftWebURL,
			&item.App.DraftMCPURL,
			&item.App.HasDraftChanges,
			&item.App.SubmissionRevision,
			&item.App.SubmissionNote,
			&item.App.SubmittedAt,
			&item.App.ReviewedBy,
			&item.App.ReviewedAt,
			&item.App.ReviewNote,
			&item.App.AdminSuspended,
			&item.App.SuspendedReason,
			&item.App.SuspendedAt,
			&item.App.LastVerifiedAt,
			&rawReport,
			&item.App.CreatedAt,
			&item.App.UpdatedAt,
			&item.ReviewPreview,
		); err != nil {
			return nil, err
		}
		if len(rawReport) > 0 && string(rawReport) != "null" {
			var rep domain.MarketplaceEndpointVerification
			if uerr := json.Unmarshal(rawReport, &rep); uerr == nil {
				item.App.VerificationReport = &rep
			}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Repository) InstallWorkspaceMarketplaceApp(ctx context.Context, workspaceID, appID, installedBy string, reviewPreview bool) error {
	_, err := r.DB.Conn(ctx).Exec(
		ctx,
		`INSERT INTO workspace_marketplace_apps (workspace_id, app_id, installed_by, review_preview)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (workspace_id, app_id) DO UPDATE SET review_preview = EXCLUDED.review_preview`,
		workspaceID,
		appID,
		installedBy,
		reviewPreview,
	)
	return err
}

func (r *Repository) UninstallWorkspaceMarketplaceApp(ctx context.Context, workspaceID, appID string) error {
	_, err := r.DB.Conn(ctx).Exec(
		ctx,
		`DELETE FROM workspace_marketplace_apps WHERE workspace_id=$1 AND app_id=$2`,
		workspaceID,
		appID,
	)
	return err
}

func (r *Repository) GetInstalledWorkspaceMarketplaceAppForSession(ctx context.Context, workspaceID, appID string) (domain.InstalledWorkspaceMarketplaceApp, error) {
	var item domain.InstalledWorkspaceMarketplaceApp
	var rawReport []byte
	err := r.DB.Conn(ctx).QueryRow(
		ctx,
		`SELECT `+marketplaceAppPrefixedSafeColumns+`, a.signing_secret, w.review_preview
		 FROM workspace_marketplace_apps w
		 JOIN marketplace_apps a ON a.id=w.app_id
		 WHERE w.workspace_id=$1 AND w.app_id=$2`,
		workspaceID,
		appID,
	).Scan(
		&item.App.ID,
		&item.App.Slug,
		&item.App.Name,
		&item.App.Description,
		&item.App.WebURL,
		&item.App.MCPURL,
		&item.App.Enabled,
		&item.App.OwnerID,
		&item.App.IsPublished,
		&item.App.PublishedVersion,
		&item.App.ReviewStatus,
		&item.App.DraftName,
		&item.App.DraftDescription,
		&item.App.DraftWebURL,
		&item.App.DraftMCPURL,
		&item.App.HasDraftChanges,
		&item.App.SubmissionRevision,
		&item.App.SubmissionNote,
		&item.App.SubmittedAt,
		&item.App.ReviewedBy,
		&item.App.ReviewedAt,
		&item.App.ReviewNote,
		&item.App.AdminSuspended,
		&item.App.SuspendedReason,
		&item.App.SuspendedAt,
		&item.App.LastVerifiedAt,
		&rawReport,
		&item.App.CreatedAt,
		&item.App.UpdatedAt,
		&item.App.SigningSecret,
		&item.ReviewPreview,
	)
	if err != nil {
		return domain.InstalledWorkspaceMarketplaceApp{}, notFound(err)
	}
	if len(rawReport) > 0 && string(rawReport) != "null" {
		var rep domain.MarketplaceEndpointVerification
		if uerr := json.Unmarshal(rawReport, &rep); uerr == nil {
			item.App.VerificationReport = &rep
		}
	}
	return item, nil
}
