// Package pg implements the PostgreSQL outbound repository and transaction
// manager for the Agent Control Plane bounded context.
package pg

import (
	"context"
	_ "embed"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/pkg/postgres"
	"teamie/services/agent-control/internal/domain"
)

//go:embed migrations/001_agents.sql
var SchemaV1 string

//go:embed migrations/002_agent_model_id.sql
var SchemaV2 string

// Migrate applies all Agent Control Plane PostgreSQL schema migrations under advisory lock 704201.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if err := postgres.Migrate(ctx, pool, 704201, "agent_control_schema_migrations", []postgres.Migration{
		{Version: 1, SQL: SchemaV1},
		{Version: 2, SQL: SchemaV2},
	}); err != nil {
		return domain.ErrSchemaUnavailable(err)
	}
	return nil
}

const columns = "id,owner_id,name,description,model_id,workspace_ids,enabled,revision,created_at,updated_at,deleted_at,runtime_state,last_active_at,last_error,membership_revision,applied_revision,applied_membership_revision,policy_enabled"

type scanner interface {
	Scan(...any) error
}

func scanAgent(row scanner) (domain.Agent, error) {
	var a domain.Agent
	err := row.Scan(
		&a.ID,
		&a.OwnerID,
		&a.Name,
		&a.Description,
		&a.ModelID,
		&a.WorkspaceIDs,
		&a.Enabled,
		&a.Revision,
		&a.CreatedAt,
		&a.UpdatedAt,
		&a.DeletedAt,
		&a.RuntimeState,
		&a.LastActiveAt,
		&a.LastError,
		&a.MembershipRevision,
		&a.AppliedRevision,
		&a.AppliedMembershipRevision,
		&a.PolicyEnabled,
	)
	a.PrincipalID = a.ID
	a.SessionID = "agent:" + a.ID
	if a.WorkspaceIDs == nil {
		a.WorkspaceIDs = []string{}
	}
	if postgres.IsNoRows(err) {
		err = domain.ErrNotFound("Agent is unavailable")
	}
	return a, err
}

// Repository implements domain.TxManager and domain.AgentRepository on top of postgres.DB.
type Repository struct {
	db *postgres.DB
}

var (
	_ domain.TxManager       = (*Repository)(nil)
	_ domain.AgentRepository = (*Repository)(nil)
)

// New constructs a PostgreSQL Repository bound to db.
func New(db *postgres.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return r.db.WithinTx(ctx, fn)
}

func (r *Repository) Ping(ctx context.Context) error {
	return r.db.Ping(ctx)
}

func (r *Repository) Insert(ctx context.Context, a domain.Agent) (domain.Agent, error) {
	return scanAgent(r.db.Conn(ctx).QueryRow(
		ctx,
		"INSERT INTO agents(id,owner_id,name,description,model_id,workspace_ids,enabled,policy_enabled) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING "+columns,
		a.ID, a.OwnerID, a.Name, a.Description, a.ModelID, a.WorkspaceIDs, a.Enabled, a.PolicyEnabled,
	))
}

func (r *Repository) GetOwned(ctx context.Context, ownerID, id string) (domain.Agent, error) {
	return scanAgent(r.db.Conn(ctx).QueryRow(
		ctx,
		"SELECT "+columns+" FROM agents WHERE id=$1 AND owner_id=$2 AND deleted_at IS NULL",
		id, ownerID,
	))
}

func (r *Repository) GetForUpdate(ctx context.Context, ownerID, id string) (domain.Agent, error) {
	return scanAgent(r.db.Conn(ctx).QueryRow(
		ctx,
		"SELECT "+columns+" FROM agents WHERE id=$1 AND owner_id=$2 AND deleted_at IS NULL FOR UPDATE",
		id, ownerID,
	))
}

func (r *Repository) GetByIDForUpdate(ctx context.Context, id string) (domain.Agent, error) {
	return scanAgent(r.db.Conn(ctx).QueryRow(
		ctx,
		"SELECT "+columns+" FROM agents WHERE id=$1 FOR UPDATE",
		id,
	))
}

func (r *Repository) UpdateProfile(ctx context.Context, a domain.Agent) error {
	_, err := r.db.Conn(ctx).Exec(
		ctx,
		"UPDATE agents SET name=$2,description=$3,model_id=$4,workspace_ids=$5,enabled=$6,membership_revision=$7,revision=revision+1,runtime_state=CASE WHEN $6 THEN 'provisioning' ELSE 'disabled' END,last_error='',updated_at=now() WHERE id=$1",
		a.ID, a.Name, a.Description, a.ModelID, a.WorkspaceIDs, a.Enabled, a.MembershipRevision,
	)
	return err
}

func (r *Repository) MarkDeleted(ctx context.Context, ownerID, id string) (bool, error) {
	tag, err := r.db.Conn(ctx).Exec(
		ctx,
		"UPDATE agents SET enabled=false,workspace_ids='{}',deleted_at=COALESCE(deleted_at,now()),revision=revision+1,membership_revision=membership_revision+1,runtime_state='disabled',last_error='',updated_at=now() WHERE id=$1 AND owner_id=$2",
		id, ownerID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (r *Repository) ListWhere(ctx context.Context, where string, args ...any) ([]domain.Agent, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, "SELECT "+columns+" FROM agents "+where+" ORDER BY created_at,id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) ListOwned(ctx context.Context, ownerID string) ([]domain.Agent, error) {
	return r.ListWhere(ctx, "WHERE owner_id=$1 AND deleted_at IS NULL", ownerID)
}

func (r *Repository) ListWorkspaceAgents(ctx context.Context, workspaceID string, participantIDs []string) ([]domain.Agent, error) {
	return r.ListWhere(ctx, "WHERE $1=ANY(workspace_ids) AND id=ANY($2) AND deleted_at IS NULL", workspaceID, participantIDs)
}

func (r *Repository) ListAll(ctx context.Context) ([]domain.Agent, error) {
	return r.ListWhere(ctx, "")
}

func (r *Repository) GetByID(ctx context.Context, id string) (domain.Agent, bool, error) {
	all, err := r.ListWhere(ctx, "WHERE id=$1", id)
	if err != nil {
		return domain.Agent{}, false, err
	}
	if len(all) == 0 {
		return domain.Agent{}, false, nil
	}
	return all[0], true, nil
}

func (r *Repository) UpdatePolicyEnabled(ctx context.Context, id string, policyEnabled bool) error {
	_, err := r.db.Conn(ctx).Exec(
		ctx,
		"UPDATE agents SET policy_enabled=$2,revision=revision+1,updated_at=now() WHERE id=$1",
		id, policyEnabled,
	)
	return err
}

func (r *Repository) UpdateWorkspaces(ctx context.Context, id string, workspaceIDs []string) error {
	_, err := r.db.Conn(ctx).Exec(
		ctx,
		"UPDATE agents SET workspace_ids=$2,revision=revision+1,membership_revision=membership_revision+1,updated_at=now() WHERE id=$1",
		id, workspaceIDs,
	)
	return err
}

func (r *Repository) MarkReconciled(ctx context.Context, id string, enabled bool) error {
	_, err := r.db.Conn(ctx).Exec(
		ctx,
		"UPDATE agents SET applied_revision=revision,applied_membership_revision=membership_revision,runtime_state=CASE WHEN $2 THEN CASE WHEN runtime_state IN ('provisioning','disabled','error') THEN 'idle' ELSE runtime_state END ELSE 'disabled' END,last_error='' WHERE id=$1",
		id, enabled,
	)
	return err
}

func (r *Repository) MarkReconcileFailed(ctx context.Context, id string) error {
	_, err := r.db.Conn(ctx).Exec(
		ctx,
		"UPDATE agents SET runtime_state=CASE WHEN enabled AND policy_enabled AND deleted_at IS NULL THEN 'error' ELSE 'disabled' END,last_error='im_unavailable' WHERE id=$1",
		id,
	)
	return err
}

func (r *Repository) ReportRuntime(ctx context.Context, agentID, runtimeState string, active *time.Time, safeErr string) (bool, error) {
	tag, err := r.db.Conn(ctx).Exec(
		ctx,
		"UPDATE agents SET runtime_state=CASE WHEN NOT enabled OR NOT policy_enabled OR deleted_at IS NOT NULL THEN 'disabled' ELSE $2 END,last_active_at=COALESCE($3,last_active_at),last_error=$4 WHERE id=$1",
		agentID, runtimeState, active, safeErr,
	)
	return err == nil && tag.RowsAffected() > 0, err
}
