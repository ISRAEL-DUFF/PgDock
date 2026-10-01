package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/israel-duff/pgdock/internal/store"
)

// KindNoop is a dummy operation for exercising the queue end to end.
const KindNoop = "noop"

// NoopParams configures a noop operation.
type NoopParams struct {
	// Steps is the number of log steps to emit (default 3, max 100).
	Steps int `json:"steps"`
	// DelayMS is the pause between steps (default 500, max 10000).
	DelayMS int `json:"delay_ms"`
	// FailAttempts makes the first N attempts fail, to exercise retries.
	FailAttempts int `json:"fail_attempts"`
}

// Noop returns the noop kind.
func Noop() Kind {
	return Kind{Handler: runNoop, MaxAttempts: 3, Timeout: 5 * time.Minute}
}

func runNoop(ctx context.Context, op store.Operation, log *StepLogger) error {
	p := NoopParams{Steps: 3, DelayMS: 500}
	if err := json.Unmarshal(op.Params, &p); err != nil {
		return Permanent(fmt.Errorf("invalid params: %w", err))
	}
	if p.Steps <= 0 || p.Steps > 100 || p.DelayMS < 0 || p.DelayMS > 10000 {
		return Permanent(fmt.Errorf("params out of range: steps 1-100, delay_ms 0-10000"))
	}

	for i := 1; i <= p.Steps; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(p.DelayMS) * time.Millisecond):
		}
		if err := log.Info(ctx, fmt.Sprintf("step-%d", i), "step %d of %d done", i, p.Steps); err != nil {
			return err
		}
		if i == (p.Steps+1)/2 && int(op.Attempts) <= p.FailAttempts {
			return fmt.Errorf("simulated failure on attempt %d", op.Attempts)
		}
	}
	return nil
}
