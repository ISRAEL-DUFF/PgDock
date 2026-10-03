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

// DefaultDedicatedSettings are the dedicated-tier defaults from spec §4.3
// for an instance with volumeGB of disk: most of the instance's 100
// connections, a larger pool, no timeouts, a disk warning at 80% of the
// volume, and a read-only SQL console (spec §16 #6).
func DefaultDedicatedSettings(volumeGB int) ProjectSettings {
	return ProjectSettings{
		ConnectionLimit: 90,
		PoolSize:        20,
		DiskWarnBytes:   int64(volumeGB) << 30 * 8 / 10,
		ConsoleReadOnly: true,
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

// OrgSettings is an organisation's settings column (V2 §11).
type OrgSettings struct {
	// MembersCanCreateProjects lets org members create projects, becoming
	// their admin (V2 §2.2). On unless turned off.
	MembersCanCreateProjects bool `json:"members_can_create_projects"`
	// SensitiveByDefault marks new projects "contains sensitive data" (V2
	// §8.5): their branches default to schema only.
	SensitiveByDefault bool `json:"sensitive_by_default,omitempty"`
}

// DefaultOrgSettings are the settings of a new organisation.
func DefaultOrgSettings() OrgSettings { return OrgSettings{MembersCanCreateProjects: true} }

// DecodeOrgSettings parses an organisation's settings, filling unset fields
// from DefaultOrgSettings.
func DecodeOrgSettings(raw json.RawMessage) (OrgSettings, error) {
	s := DefaultOrgSettings()
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("decode org settings: %w", err)
	}
	return s, nil
}
