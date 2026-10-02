package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/store"
)

// KindReclaimSpace rewrites one table to return the space deleted rows
// still hold (V2 §10.4 Reclaim space).
const KindReclaimSpace = "reclaim_space"

type reclaimParams struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
}

// ReclaimSpace queues VACUUM FULL of schema.table. It locks the table while
// it runs. It is allowed while the project is locked, which is when it is
// needed.
func (s *Service) ReclaimSpace(ctx context.Context, projectID uuid.UUID, schema, table string, by *uuid.UUID) (store.Operation, error) {
	if schema == "" || table == "" || len(schema) > 63 || len(table) > 63 {
		return store.Operation{}, fmt.Errorf("%w: a schema and table are required", ErrInvalid)
	}
	return s.EnqueueExclusive(ctx, projectID, []string{StatusActive}, "", KindReclaimSpace,
		reclaimParams{Schema: schema, Table: table}, by, nil)
}

func (s *Service) runReclaimSpace(ctx context.Context, op store.Operation, log *jobs.StepLogger) error {
	p, err := s.project(ctx, op)
	if err != nil {
		return err
	}
	var params reclaimParams
	if err := json.Unmarshal(op.Params, &params); err != nil {
		return jobs.Permanent(fmt.Errorf("invalid params: %w", err))
	}
	conn, err := s.connectInstance(ctx, p.InstanceID, p.DbName)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	rel := pgx.Identifier{params.Schema, params.Table}.Sanitize()
	var before int64
	err = conn.QueryRow(ctx, `SELECT pg_total_relation_size(c.oid) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r', 'm')`, params.Schema, params.Table).Scan(&before)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Permanent(fmt.Errorf("%s is not a table in this project", rel))
	}
	if err != nil {
		return err
	}
	if err := log.Info(ctx, "vacuum", "VACUUM FULL %s (%d bytes); the table is locked until it finishes", rel, before); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "VACUUM (FULL, ANALYZE) "+rel); err != nil {
		return err
	}
	var after int64
	if err := conn.QueryRow(ctx, "SELECT pg_total_relation_size($1::regclass)", rel).Scan(&after); err != nil {
		return err
	}
	return log.Info(ctx, "done", "%s is %d bytes (was %d); storage locks lift at the next size check", rel, after, before)
}
