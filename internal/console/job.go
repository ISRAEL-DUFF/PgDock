package console

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
)

// RunJob runs a scheduled SQL job's script (V2 §9.2) as the project's
// owner, in one transaction, through the console login: like the console,
// the script cannot RESET ROLE its way to the superuser an admin
// connection is. Unlike the console, the database's read-only default
// stays, so a soft storage lock applies. It returns the rows the
// statements affected.
func (s *Service) RunJob(ctx context.Context, p store.Project, sqlText string, timeout time.Duration) (int64, error) {
	sess, err := s.open(ctx, p, uuid.New(), timeout, modeJob)
	if err != nil {
		return 0, err
	}
	defer sess.close()
	var rows int64
	err = pgx.BeginFunc(ctx, sess.conn, func(tx pgx.Tx) error {
		results, err := tx.Conn().PgConn().Exec(ctx, sqlText).ReadAll()
		if err != nil {
			return err
		}
		for _, r := range results {
			if r.Err != nil {
				return r.Err
			}
			rows += r.CommandTag.RowsAffected()
		}
		return nil
	})
	return rows, err
}
