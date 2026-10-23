package pg

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"teamie/services/agent-runtime/internal/domain"
)

// WithinFencedTx runs fn inside a database transaction fenced by lease if present.
func (r *Repository) WithinFencedTx(ctx context.Context, lease *domain.Lease, fn func(txCtx context.Context) error) error {
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		if lease != nil {
			if err := r.CheckLeaseForUpdate(txCtx, lease); err != nil {
				return err
			}
		}
		return fn(txCtx)
	})
}

// LoadTaskRowForUpdate locks and loads a task snapshot and lease timestamp from runtime_tasks.
func (r *Repository) LoadTaskRowForUpdate(ctx context.Context, agentID, taskID string) ([]byte, *time.Time, time.Time, error) {
	var b []byte
	var until *time.Time
	var now time.Time
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT snapshot,lease_until,clock_timestamp() FROM runtime_tasks WHERE id=$1 AND agent_id=$2 FOR UPDATE`, taskID, agentID).Scan(&b, &until, &now)
	return b, until, now, err
}

// InsertTaskRow inserts a new task row into runtime_tasks, returning whether it was inserted.
func (r *Repository) InsertTaskRow(ctx context.Context, taskID, agentID string, runID *string, snapshot []byte) (bool, error) {
	tag, err := r.db.Conn(ctx).Exec(ctx, `INSERT INTO runtime_tasks(id,agent_id,run_id,snapshot) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, taskID, agentID, runID, snapshot)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// UpdateTaskRow updates a task snapshot and lease_until timestamp in runtime_tasks.
func (r *Repository) UpdateTaskRow(ctx context.Context, taskID, agentID string, snapshot []byte, until *time.Time) error {
	_, err := r.db.Conn(ctx).Exec(ctx, `UPDATE runtime_tasks SET snapshot=$3,lease_until=$4 WHERE id=$1 AND agent_id=$2`, taskID, agentID, snapshot, until)
	return err
}

// InsertTaskNotificationRow inserts a notification snapshot into runtime_task_notifications.
func (r *Repository) InsertTaskNotificationRow(ctx context.Context, id, agentID, taskID string, snapshot []byte) error {
	_, err := r.db.Conn(ctx).Exec(ctx, `INSERT INTO runtime_task_notifications(id,agent_id,task_id,snapshot) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, id, agentID, taskID, snapshot)
	return err
}

// InsertTaskPublicEvent records a task.status event in runtime_public_events.
func (r *Repository) InsertTaskPublicEvent(ctx context.Context, taskID, agentID, status string) error {
	_, err := r.db.Conn(ctx).Exec(ctx, `INSERT INTO runtime_public_events(agent_id,type,run_id,task_id,status) SELECT agent_id,'task.status',run_id,id,$3 FROM runtime_tasks WHERE id=$1 AND agent_id=$2`, taskID, agentID, status)
	return err
}

// ListTaskIDsAfter returns ordered task IDs for agentID greater than lastID.
func (r *Repository) ListTaskIDsAfter(ctx context.Context, agentID, lastID string) ([]string, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT id FROM runtime_tasks WHERE agent_id=$1 AND id>$2 ORDER BY id`, agentID, lastID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// UpsertTaskEventRow inserts a task event if absent and returns the stored payload and timestamp.
func (r *Repository) UpsertTaskEventRow(ctx context.Context, taskID, eventID string, payload []byte) (bool, []byte, time.Time, error) {
	conn := r.db.Conn(ctx)
	if payload == nil {
		payload = []byte{}
	}
	tag, err := conn.Exec(ctx, `INSERT INTO runtime_task_events(task_id,event_id,payload) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, taskID, eventID, payload)
	if err != nil {
		return false, nil, time.Time{}, err
	}
	var stored []byte
	var createdAt time.Time
	if err = conn.QueryRow(ctx, `SELECT payload,created_at FROM runtime_task_events WHERE task_id=$1 AND event_id=$2`, taskID, eventID).Scan(&stored, &createdAt); err != nil {
		return false, nil, time.Time{}, err
	}
	return tag.RowsAffected() == 1, stored, createdAt, nil
}

// MaxTaskEventSeq returns the maximum sequence number in runtime_task_events for taskID.
func (r *Repository) MaxTaskEventSeq(ctx context.Context, taskID string) (int64, error) {
	var maxSeq int64
	err := r.db.Conn(ctx).QueryRow(ctx, `SELECT COALESCE(max(seq),0) FROM runtime_task_events WHERE task_id=$1`, taskID).Scan(&maxSeq)
	return maxSeq, err
}

// ListTaskEventRows queries paginated task events from runtime_task_events.
func (r *Repository) ListTaskEventRows(ctx context.Context, taskID string, endSeq, posSeq int64, newestFirst bool, limit int) ([]domain.TaskEventRow, error) {
	cmp, order := ">", "ASC"
	if newestFirst {
		cmp, order = "<", "DESC"
	}
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT seq,event_id,payload,created_at FROM runtime_task_events WHERE task_id=$1 AND seq<=$2 AND seq `+cmp+` $3 ORDER BY seq `+order+` LIMIT $4`, taskID, endSeq, posSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TaskEventRow{}
	for rows.Next() {
		var row domain.TaskEventRow
		if err = rows.Scan(&row.Seq, &row.EventID, &row.Data, &row.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// UpsertCustomNotificationRow inserts a custom task notification if absent and returns the stored kind and data.
func (r *Repository) UpsertCustomNotificationRow(ctx context.Context, taskID, eventID, kind string, data []byte) (bool, string, []byte, error) {
	conn := r.db.Conn(ctx)
	if data == nil {
		data = []byte{}
	}
	tag, err := conn.Exec(ctx, `INSERT INTO runtime_custom_notifications(task_id,event_id,kind,data) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, taskID, eventID, kind, data)
	if err != nil {
		return false, "", nil, err
	}
	if tag.RowsAffected() == 1 {
		return true, kind, data, nil
	}
	var storedKind string
	var storedData []byte
	if err = conn.QueryRow(ctx, `SELECT kind,data FROM runtime_custom_notifications WHERE task_id=$1 AND event_id=$2`, taskID, eventID).Scan(&storedKind, &storedData); err != nil {
		return false, "", nil, err
	}
	return false, storedKind, storedData, nil
}

// ClaimNotificationRowsForUpdate locks and leases up to limit unacked notifications for agentID.
func (r *Repository) ClaimNotificationRowsForUpdate(ctx context.Context, agentID string, limit int, leaseSeconds float64) ([]domain.NotificationDeliveryRow, error) {
	conn := r.db.Conn(ctx)
	rows, err := conn.Query(ctx, `SELECT id,snapshot FROM runtime_task_notifications WHERE agent_id=$1 AND NOT acked AND (lease_until IS NULL OR lease_until<=clock_timestamp()) ORDER BY seq FOR UPDATE SKIP LOCKED LIMIT $2`, agentID, limit)
	if err != nil {
		return nil, err
	}
	items := []domain.NotificationDeliveryRow{}
	for rows.Next() {
		var item domain.NotificationDeliveryRow
		if err = rows.Scan(&item.ID, &item.Snapshot); err != nil {
			break
		}
		item.Receipt = []byte(uuid.NewString())
		items = append(items, item)
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, d := range items {
		if _, err = conn.Exec(ctx, `UPDATE runtime_task_notifications SET receipt=$2,lease_until=clock_timestamp()+$3*interval '1 second' WHERE id=$1`, d.ID, d.Receipt, leaseSeconds); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// AckNotificationRow marks a leased notification as acknowledged.
func (r *Repository) AckNotificationRow(ctx context.Context, agentID string, receipt []byte) (bool, error) {
	tag, err := r.db.Conn(ctx).Exec(ctx, `UPDATE runtime_task_notifications SET acked=true WHERE agent_id=$1 AND receipt=$2 AND NOT acked AND lease_until>clock_timestamp()`, agentID, receipt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// PersistNotificationInput atomically allocates a notification event sequence, resolves parent task origin,
// and inserts the task.notification input into runtime_inputs.
func (r *Repository) PersistNotificationInput(
	ctx context.Context,
	lease *domain.Lease,
	agentID, taskID, notificationID, status, createdAt string,
	extractSources func(rawInputs []byte) ([]domain.InputSource, bool, error),
) (seq, parentRunID string, inserted bool, err error) {
	err = r.WithinFencedTx(ctx, lease, func(txCtx context.Context) error {
		conn := r.db.Conn(txCtx)
		var raw []byte
		if qErr := conn.QueryRow(txCtx, `SELECT (90000000000000000000::numeric+nextval('runtime_notification_input_seq'))::text`).Scan(&seq); qErr != nil {
			return qErr
		}
		if qErr := conn.QueryRow(txCtx, `SELECT COALESCE(t.run_id::text,''),COALESCE(r.input_json,'[]'::jsonb)
FROM runtime_tasks t LEFT JOIN runtime_runs r ON r.id=t.run_id AND r.agent_id=t.agent_id
WHERE t.agent_id=$1 AND t.id=$2`, agentID, taskID).Scan(&parentRunID, &raw); qErr != nil {
			return qErr
		}
		refs, overflow, qErr := extractSources(raw)
		if qErr != nil {
			return qErr
		}
		in := domain.Input{
			EventID:        seq,
			Kind:           "task.notification",
			TaskID:         taskID,
			NotificationID: notificationID,
			Status:         status,
			CreatedAt:      createdAt,
			ParentRunID:    parentRunID,
			SourceRefs:     refs,
			SourceOverflow: overflow,
		}
		b, _ := json.Marshal(in)
		tag, insertErr := conn.Exec(txCtx, `INSERT INTO runtime_inputs(agent_id,event_id,payload,notification_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, agentID, seq, b, notificationID)
		if insertErr != nil {
			return insertErr
		}
		inserted = tag.RowsAffected() > 0
		return nil
	})
	if err != nil {
		return "", "", false, err
	}
	return seq, parentRunID, inserted, nil
}

// ListIncompleteAgentTaskIDs returns IDs of non-completed tasks for agentID.
func (r *Repository) ListIncompleteAgentTaskIDs(ctx context.Context, agentID string) ([]string, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT id FROM runtime_tasks WHERE agent_id=$1 AND snapshot->>'Status'<>'completed'`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var taskID string
		if rows.Scan(&taskID) == nil {
			ids = append(ids, taskID)
		}
	}
	return ids, nil
}

// ListIncompleteRunTaskIDs returns IDs of non-completed tasks for (agentID, runID).
func (r *Repository) ListIncompleteRunTaskIDs(ctx context.Context, agentID, runID string) ([]string, error) {
	rows, err := r.db.Conn(ctx).Query(ctx, `SELECT id FROM runtime_tasks WHERE agent_id=$1 AND run_id=$2 AND snapshot->>'Status'<>'completed'`, agentID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
