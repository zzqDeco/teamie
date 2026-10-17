package control

import (
	"context"
	_ "embed"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"slices"
	"strings"
	"time"
)

//go:embed schema.sql
var schema string

type Store struct {
	DB           *pgxpool.Pool
	IM           Authority
	Flags        Flags
	RuntimeToken string
}

func Open(ctx context.Context, url string, im Authority, flags Flags) (*Store, error) {
	cfg, e := pgxpool.ParseConfig(url)
	if e != nil {
		return nil, e
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		c.TypeMap().RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID, Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC}})
		return nil
	}
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		return nil, e
	}
	s := &Store{DB: db, IM: im, Flags: flags}
	if e = s.Migrate(ctx); e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) Migrate(ctx context.Context) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(704201)"); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS agent_control_schema_migrations(version integer PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())"); e != nil {
		return e
	}
	var n int
	if e = tx.QueryRow(ctx, "SELECT COALESCE(max(version),0) FROM agent_control_schema_migrations").Scan(&n); e != nil {
		return e
	}
	if n > 1 {
		return errAPI(503, "schema_unavailable", "Unsupported control schema")
	}
	if n == 0 {
		if _, e = tx.Exec(ctx, schema); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO agent_control_schema_migrations(version) VALUES(1)"); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

const columns = "id,owner_id,name,description,workspace_ids,enabled,revision,created_at,updated_at,deleted_at,runtime_state,last_active_at,last_error,membership_revision,applied_revision,applied_membership_revision,policy_enabled"

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Agent, error) {
	var a Agent
	e := row.Scan(&a.ID, &a.OwnerID, &a.Name, &a.Description, &a.WorkspaceIDs, &a.Enabled, &a.Revision, &a.CreatedAt, &a.UpdatedAt, &a.DeletedAt, &a.RuntimeState, &a.LastActiveAt, &a.LastError, &a.MembershipRevision, &a.AppliedRevision, &a.AppliedMembershipRevision, &a.PolicyEnabled)
	a.PrincipalID = a.ID
	a.SessionID = "agent:" + a.ID
	if a.WorkspaceIDs == nil {
		a.WorkspaceIDs = []string{}
	}
	if errors.Is(e, pgx.ErrNoRows) {
		e = errAPI(404, "not_found", "Agent is unavailable")
	}
	return a, e
}
func (s *Store) flag(key string) error {
	if !s.Flags.Enabled(key) {
		return errAPI(403, "feature_disabled", "This Agent action is currently disabled")
	}
	return nil
}
func (s *Store) validateChanges(ctx context.Context, owner string, in Changes, create bool) error {
	if create && (in.Name == nil || in.WorkspaceIDs == nil) {
		return errAPI(400, "invalid_request", "Name and workspace IDs are required")
	}
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if !bounded(*in.Name, 1, 80) {
			return errAPI(400, "invalid_name", "Agent name must contain 1 to 80 characters")
		}
	}
	if in.Description != nil && len([]rune(*in.Description)) > 1000 {
		return errAPI(400, "invalid_description", "Description must contain at most 1000 characters")
	}
	if in.WorkspaceIDs != nil {
		if len(*in.WorkspaceIDs) > 100 {
			return errAPI(400, "invalid_workspaces", "Select at most 100 workspaces")
		}
		*in.WorkspaceIDs = normalized(*in.WorkspaceIDs)
		allowed, e := s.IM.Workspaces(ctx, owner)
		if e != nil {
			return e
		}
		set := map[string]bool{}
		for _, id := range allowed {
			set[id] = true
		}
		for _, id := range *in.WorkspaceIDs {
			if !set[id] {
				return errAPI(403, "forbidden", "Only current workspace memberships may be selected")
			}
		}
	}
	return nil
}
func (s *Store) Create(ctx context.Context, owner string, in Changes) (Agent, error) {
	if e := s.flag("agents.enabled"); e != nil {
		return Agent{}, e
	}
	if e := s.flag("agents.create"); e != nil {
		return Agent{}, e
	}
	if e := s.validateChanges(ctx, owner, in, true); e != nil {
		return Agent{}, e
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	description := ""
	if in.Description != nil {
		description = *in.Description
	}
	a, e := scan(s.DB.QueryRow(ctx, "INSERT INTO agents(id,owner_id,name,description,workspace_ids,enabled,policy_enabled) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING "+columns, id(), owner, *in.Name, description, *in.WorkspaceIDs, enabled, s.Flags.Enabled("agents.enabled")))
	if e != nil {
		return a, e
	}
	_ = s.Reconcile(ctx, a.ID)
	return s.Owned(ctx, owner, a.ID)
}
func (s *Store) Owned(ctx context.Context, owner, id string) (Agent, error) {
	return scan(s.DB.QueryRow(ctx, "SELECT "+columns+" FROM agents WHERE id=$1 AND owner_id=$2 AND deleted_at IS NULL", id, owner))
}
func (s *Store) Update(ctx context.Context, owner, id string, in Changes) (Agent, error) {
	if e := s.flag("agents.update"); e != nil {
		return Agent{}, e
	}
	if in.Name == nil && in.Description == nil && in.WorkspaceIDs == nil && in.Enabled == nil {
		return Agent{}, errAPI(400, "invalid_request", "No Agent changes provided")
	}
	if in.Enabled != nil && *in.Enabled {
		if e := s.flag("agents.enabled"); e != nil {
			return Agent{}, e
		}
	}
	if e := s.validateChanges(ctx, owner, in, false); e != nil {
		return Agent{}, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Agent{}, e
	}
	defer tx.Rollback(ctx)
	a, e := scan(tx.QueryRow(ctx, "SELECT "+columns+" FROM agents WHERE id=$1 AND owner_id=$2 AND deleted_at IS NULL FOR UPDATE", id, owner))
	if e != nil {
		return a, e
	}
	if in.Name != nil {
		a.Name = *in.Name
	}
	if in.Description != nil {
		a.Description = *in.Description
	}
	if in.WorkspaceIDs != nil && !slices.Equal(normalized(a.WorkspaceIDs), *in.WorkspaceIDs) {
		a.WorkspaceIDs = *in.WorkspaceIDs
		a.MembershipRevision++
	}
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
	}
	_, e = tx.Exec(ctx, "UPDATE agents SET name=$2,description=$3,workspace_ids=$4,enabled=$5,membership_revision=$6,revision=revision+1,runtime_state=CASE WHEN $5 THEN 'provisioning' ELSE 'disabled' END,last_error='',updated_at=now() WHERE id=$1", id, a.Name, a.Description, a.WorkspaceIDs, a.Enabled, a.MembershipRevision)
	if e != nil {
		return a, e
	}
	if e = tx.Commit(ctx); e != nil {
		return a, e
	}
	e = s.Reconcile(ctx, id)
	if e != nil && in.Enabled != nil && !*in.Enabled {
		return a, errAPI(503, "control_unavailable", "Disable is pending while IM authority is unavailable")
	}
	return s.Owned(ctx, owner, id)
}
func (s *Store) Delete(ctx context.Context, owner, id string) error {
	if e := s.flag("agents.delete"); e != nil {
		return e
	}
	tag, e := s.DB.Exec(ctx, "UPDATE agents SET enabled=false,workspace_ids='{}',deleted_at=COALESCE(deleted_at,now()),revision=revision+1,membership_revision=membership_revision+1,runtime_state='disabled',last_error='',updated_at=now() WHERE id=$1 AND owner_id=$2", id, owner)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return errAPI(404, "not_found", "Agent is unavailable")
	}
	if e = s.Reconcile(ctx, id); e != nil {
		return errAPI(503, "control_unavailable", "Deletion is pending while IM authority is unavailable")
	}
	return nil
}
func (s *Store) list(ctx context.Context, where string, args ...any) ([]Agent, error) {
	rows, e := s.DB.Query(ctx, "SELECT "+columns+" FROM agents "+where+" ORDER BY created_at,id", args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		a, e := scan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) ListOwned(ctx context.Context, owner string) ([]Agent, error) {
	return s.list(ctx, "WHERE owner_id=$1 AND deleted_at IS NULL", owner)
}
func (s *Store) Discover(ctx context.Context, workspace, user string) ([]Agent, error) {
	ids, e := s.IM.Participants(ctx, workspace, user)
	if e != nil {
		return nil, e
	}
	if !s.Flags.Enabled("agents.discover") {
		return []Agent{}, nil
	}
	return s.list(ctx, "WHERE $1=ANY(workspace_ids) AND id=ANY($2) AND deleted_at IS NULL", workspace, ids)
}
func (s *Store) decorate(ctx context.Context, agents []Agent) ([]Agent, error) {
	ids := []string{}
	for _, a := range agents {
		ids = append(ids, a.OwnerID)
	}
	profiles, e := s.IM.Users(ctx, ids)
	if e != nil {
		return nil, e
	}
	for n := range agents {
		if p := profiles[agents[n].OwnerID]; p != nil {
			agents[n].OwnerName = p.DisplayName
		}
		if !agents[n].Enabled || !s.Flags.Enabled("agents.enabled") || agents[n].DeletedAt != nil {
			agents[n].RuntimeState = "disabled"
		}
	}
	return agents, nil
}
