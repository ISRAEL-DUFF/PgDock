package store

import (
	"encoding/json"
	"fmt"
)

// ProjectSettings is the projects.settings column: per-project guardrails
// (spec §4.3), all editable per project.
type ProjectSettings struct {
	// ConnectionLimit caps backend connections for the owner role.
	ConnectionLimit int `json:"connection_limit"`
	// PoolSize is the pooler's default pool size for the project.
	PoolSize int `json:"pool_size"`
	// StatementTimeout and IdleInTransactionTimeout are Postgres interval
	// strings ("60s"); empty means unset.
	StatementTimeout         string `json:"statement_timeout"`
	IdleInTransactionTimeout string `json:"idle_in_transaction_session_timeout"`
	// DiskWarnBytes triggers the soft disk quota alert.
	DiskWarnBytes int64 `json:"disk_warn_bytes"`
	// ConsoleReadOnly makes the SQL console read-only.
	ConsoleReadOnly bool `json:"console_read_only"`
}

// DefaultSharedSettings are the shared-tier defaults from spec §4.3.
func DefaultSharedSettings() ProjectSettings {
	return ProjectSettings{
		ConnectionLimit:          20,
		PoolSize:                 5,
		StatementTimeout:         "60s",
		IdleInTransactionTimeout: "60s",
		DiskWarnBytes:            1 << 30,
		ConsoleReadOnly:          false,
	}
}

// DecodeProjectSettings parses a settings column, filling unset fields
// from DefaultSharedSettings.
func DecodeProjectSettings(raw json.RawMessage) (ProjectSettings, error) {
	s := DefaultSharedSettings()
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("decode project settings: %w", err)
	}
	return s, nil
}
