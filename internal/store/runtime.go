package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jfox/redline/internal/domain"
)

func validateRuntimeConnection(item domain.RuntimeConnection) error {
	if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Runtime) == "" || strings.TrimSpace(item.Transport) == "" {
		return fmt.Errorf("runtime connection id, runtime, and transport are required")
	}
	if item.Runtime != "hermes" {
		return fmt.Errorf("unsupported runtime %q", item.Runtime)
	}
	if item.Transport != "local" && item.Transport != "gateway" {
		return fmt.Errorf("unsupported runtime transport %q", item.Transport)
	}
	if item.Transport == "gateway" && strings.TrimSpace(item.URL) == "" && item.CredentialSource != "hermes_desktop" {
		return fmt.Errorf("gateway runtime connection requires a URL or Hermes Desktop import")
	}
	if item.MaxConcurrentRuns < 0 {
		return fmt.Errorf("runtime connection max_concurrent_runs cannot be negative")
	}
	return nil
}

func (d *DB) CreateRuntimeConnection(ctx context.Context, item domain.RuntimeConnection, now time.Time) error {
	if err := validateRuntimeConnection(item); err != nil {
		return err
	}
	if item.MaxConcurrentRuns == 0 {
		item.MaxConcurrentRuns = 1
	}
	_, err := d.db.ExecContext(ctx, `INSERT INTO runtime_connections (
id, runtime, transport, url, credential_source, credential_ref, desktop_config_path,
max_concurrent_runs, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.ID, item.Runtime, item.Transport, item.URL,
		item.CredentialSource, item.CredentialRef, item.DesktopConfigPath, item.MaxConcurrentRuns, formatTime(now))
	if err != nil {
		return fmt.Errorf("create runtime connection: %w", err)
	}
	return nil
}

func (d *DB) ListRuntimeConnections(ctx context.Context) ([]domain.RuntimeConnection, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, runtime, transport, url, credential_source,
credential_ref, desktop_config_path, max_concurrent_runs, created_at FROM runtime_connections ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list runtime connections: %w", err)
	}
	defer rows.Close()
	var result []domain.RuntimeConnection
	for rows.Next() {
		item, err := scanRuntimeConnection(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func scanRuntimeConnection(row scanner) (domain.RuntimeConnection, error) {
	var item domain.RuntimeConnection
	var created string
	if err := row.Scan(&item.ID, &item.Runtime, &item.Transport, &item.URL, &item.CredentialSource,
		&item.CredentialRef, &item.DesktopConfigPath, &item.MaxConcurrentRuns, &created); err != nil {
		return domain.RuntimeConnection{}, fmt.Errorf("scan runtime connection: %w", err)
	}
	var err error
	item.CreatedAt, err = parseStoredTime(created)
	return item, err
}

func (d *DB) GetRuntimeConnection(ctx context.Context, id string) (domain.RuntimeConnection, error) {
	item, err := scanRuntimeConnection(d.db.QueryRowContext(ctx, `SELECT id, runtime, transport, url,
credential_source, credential_ref, desktop_config_path, max_concurrent_runs, created_at
FROM runtime_connections WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RuntimeConnection{}, ErrNotFound
	}
	return item, err
}

func validateAgentContext(item domain.AgentContext) error {
	if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.RuntimeConnectionID) == "" {
		return fmt.Errorf("agent context id and runtime connection are required")
	}
	if item.SessionMode != "isolated" && item.SessionMode != "persistent" {
		return fmt.Errorf("agent context session_mode must be isolated or persistent")
	}
	if item.MaxConcurrentRuns < 0 {
		return fmt.Errorf("agent context max_concurrent_runs cannot be negative")
	}
	return nil
}

func (d *DB) CreateAgentContext(ctx context.Context, item domain.AgentContext, now time.Time) error {
	if item.SessionMode == "" {
		item.SessionMode = "isolated"
	}
	if err := validateAgentContext(item); err != nil {
		return err
	}
	if item.MaxConcurrentRuns == 0 {
		item.MaxConcurrentRuns = 1
	}
	_, err := d.db.ExecContext(ctx, `INSERT INTO agent_contexts (
id, runtime_connection_id, profile, agent, project, working_directory, session_mode,
max_concurrent_runs, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.ID, item.RuntimeConnectionID, item.Profile, item.Agent,
		item.Project, item.WorkingDirectory, item.SessionMode, item.MaxConcurrentRuns, formatTime(now))
	if err != nil {
		return fmt.Errorf("create agent context: %w", err)
	}
	return nil
}

func scanAgentContext(row scanner) (domain.AgentContext, error) {
	var item domain.AgentContext
	var created string
	if err := row.Scan(&item.ID, &item.RuntimeConnectionID, &item.Profile, &item.Agent, &item.Project,
		&item.WorkingDirectory, &item.SessionMode, &item.MaxConcurrentRuns, &created); err != nil {
		return domain.AgentContext{}, fmt.Errorf("scan agent context: %w", err)
	}
	var err error
	item.CreatedAt, err = parseStoredTime(created)
	return item, err
}

func (d *DB) GetAgentContext(ctx context.Context, id string) (domain.AgentContext, error) {
	item, err := scanAgentContext(d.db.QueryRowContext(ctx, `SELECT id, runtime_connection_id, profile,
agent, project, working_directory, session_mode, max_concurrent_runs, created_at
FROM agent_contexts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentContext{}, ErrNotFound
	}
	return item, err
}

func (d *DB) ListAgentContexts(ctx context.Context) ([]domain.AgentContext, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, runtime_connection_id, profile, agent, project,
working_directory, session_mode, max_concurrent_runs, created_at FROM agent_contexts ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list agent contexts: %w", err)
	}
	defer rows.Close()
	var result []domain.AgentContext
	for rows.Next() {
		item, err := scanAgentContext(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (d *DB) UpdateAgentContext(ctx context.Context, item domain.AgentContext) error {
	if err := validateAgentContext(item); err != nil {
		return err
	}
	if item.MaxConcurrentRuns == 0 {
		item.MaxConcurrentRuns = 1
	}
	result, err := d.db.ExecContext(ctx, `UPDATE agent_contexts SET runtime_connection_id = ?,
profile = ?, agent = ?, project = ?, working_directory = ?, session_mode = ?,
max_concurrent_runs = ? WHERE id = ?`, item.RuntimeConnectionID, item.Profile, item.Agent,
		item.Project, item.WorkingDirectory, item.SessionMode, item.MaxConcurrentRuns, item.ID)
	if err != nil {
		return fmt.Errorf("update agent context: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return fmt.Errorf("%w: agent context %q", ErrNotFound, item.ID)
	}
	return nil
}

func (d *DB) UpdateRunExternal(ctx context.Context, runID string, external domain.ExternalRun) error {
	result, err := d.db.ExecContext(ctx, `UPDATE runs SET runtime_connection_id = ?,
external_run_id = ?, external_session_id = ? WHERE id = ? AND state IN ('preparing', 'running')`,
		external.RuntimeConnectionID, external.RunID, external.SessionID, runID)
	if err != nil {
		return fmt.Errorf("record external run: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return fmt.Errorf("%w: active run %q", ErrNotFound, runID)
	}
	return nil
}
