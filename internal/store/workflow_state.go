package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OrcaxNet/vibe-forge/contracts"
)

type workflowProjectState struct {
	runID     sql.NullString
	status    string
	version   int64
	updatedAt string
}

// initProjectWorkflowTx records the no-run draft snapshot created with a
// project. It makes the distinction between legitimate waiting (no run ever
// existed) and recovering (historical evidence is incomplete) durable.
func initProjectWorkflowTx(ctx context.Context, tx *sql.Tx, projectID, now string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO project_workflow_states(project_id, workflow_run_id, status, state_version, updated_at)
		 VALUES(?, NULL, 'draft', 1, ?)
		 ON CONFLICT(project_id) DO NOTHING`,
		projectID, now)
	if err != nil {
		return fmt.Errorf("initialize project workflow state: %w", err)
	}
	return nil
}

// pointProjectWorkflowTx makes runID the latest valid workflow and advances the
// monotonic version in the same transaction as the run lifecycle write.
func pointProjectWorkflowTx(ctx context.Context, tx *sql.Tx, projectID, runID, status, now string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO project_workflow_states(project_id, workflow_run_id, status, state_version, updated_at)
		 VALUES(?, ?, ?, 1, ?)
		 ON CONFLICT(project_id) DO UPDATE SET
		   workflow_run_id = excluded.workflow_run_id,
		   status = excluded.status,
		   state_version = project_workflow_states.state_version + 1,
		   updated_at = excluded.updated_at`,
		projectID, runID, status, now)
	if err != nil {
		return fmt.Errorf("point project workflow state: %w", err)
	}
	return nil
}

func bumpWorkflowForRunTx(ctx context.Context, tx *sql.Tx, runID, status, now string) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE project_workflow_states
		    SET status = ?, state_version = state_version + 1, updated_at = ?
		  WHERE workflow_run_id = ?`,
		status, now, runID)
	if err != nil {
		return fmt.Errorf("advance workflow state: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("read workflow update count: %w", err)
	}
	if n > 0 {
		return nil
	}
	var projectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM runs WHERE id = ?`, runID).Scan(&projectID); err != nil {
		return fmt.Errorf("find workflow project: %w", err)
	}
	var currentRun sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT workflow_run_id FROM project_workflow_states WHERE project_id = ?`, projectID).
		Scan(&currentRun)
	if err == nil {
		// A late write from an older run remains in that run's history but must
		// never move the project's latest-workflow pointer backwards.
		return nil
	}
	if !isNoRows(err) {
		return fmt.Errorf("load current workflow pointer: %w", err)
	}
	return pointProjectWorkflowTx(ctx, tx, projectID, runID, status, now)
}

// bumpProjectWorkflowSnapshotTx advances the comparable project snapshot when
// state outside the run lifecycle changes, such as a manual stable preview.
// Missing legacy workflow rows are left for the evidence-based read repair.
func bumpProjectWorkflowSnapshotTx(ctx context.Context, tx *sql.Tx, projectID, now string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE project_workflow_states
		    SET state_version = state_version + 1, updated_at = ?
		  WHERE project_id = ?`,
		now, projectID)
	if err != nil {
		return fmt.Errorf("advance project workflow snapshot: %w", err)
	}
	return nil
}

func initAttemptStagesTx(ctx context.Context, tx *sql.Tx, runID, attemptID string, attempt int, now string) error {
	for _, stage := range contracts.Load().Stages.Order {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO workflow_stage_runs(
			   workflow_run_id, attempt, attempt_id, stage_key, status,
			   started_at, finished_at, updated_at, error_code)
			 VALUES(?, ?, ?, ?, 'waiting', NULL, NULL, ?, NULL)
			 ON CONFLICT(workflow_run_id, attempt, stage_key) DO NOTHING`,
			runID, attempt, attemptID, stage, now); err != nil {
			return fmt.Errorf("initialize stage %s: %w", stage, err)
		}
	}
	return nil
}

func activeAttemptTx(ctx context.Context, tx *sql.Tx, runID string) (attemptID string, attempt int, err error) {
	err = tx.QueryRowContext(ctx,
		`SELECT id, sequence
		   FROM attempts
		  WHERE run_id = ?
		  ORDER BY CASE WHEN status IN ('queued','running') THEN 0 ELSE 1 END, sequence DESC
		  LIMIT 1`, runID).Scan(&attemptID, &attempt)
	if err != nil {
		return "", 0, fmt.Errorf("load workflow attempt: %w", err)
	}
	return attemptID, attempt, nil
}

func setStageStateTx(
	ctx context.Context,
	tx *sql.Tx,
	runID, stage, status, now string,
	errorCode *string,
) error {
	attemptID, attempt, err := activeAttemptTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	return setStageStateForAttemptTx(ctx, tx, runID, attemptID, attempt, stage, status, now, errorCode)
}

func setStageStateForAttemptTx(
	ctx context.Context,
	tx *sql.Tx,
	runID, attemptID string,
	attempt int,
	stage, status, now string,
	errorCode *string,
) error {
	var started, finished any
	switch status {
	case "running":
		started = now
	case "succeeded", "failed", "cancelled":
		finished = now
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO workflow_stage_runs(
		   workflow_run_id, attempt, attempt_id, stage_key, status,
		   started_at, finished_at, updated_at, error_code)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(workflow_run_id, attempt, stage_key) DO UPDATE SET
		   status = excluded.status,
		   started_at = COALESCE(workflow_stage_runs.started_at, excluded.started_at),
		   finished_at = COALESCE(excluded.finished_at, workflow_stage_runs.finished_at),
		   updated_at = excluded.updated_at,
		   error_code = excluded.error_code`,
		runID, attempt, attemptID, stage, status, started, finished, now, nullString(errorCode))
	if err != nil {
		return fmt.Errorf("persist stage %s state: %w", stage, err)
	}
	return nil
}

func nullString(v *string) sql.NullString {
	if v == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *v, Valid: true}
}

func eventString(payload map[string]any, key string) string {
	v, _ := payload[key].(string)
	return v
}

// applyWorkflowEventTx persists lifecycle facts alongside their SSE event.
func applyWorkflowEventTx(ctx context.Context, tx *sql.Tx, runID, eventType, now string, payload map[string]any) error {
	switch eventType {
	case "run_started":
		return bumpWorkflowForRunTx(ctx, tx, runID, "running", now)
	case "stage_started":
		stage := eventString(payload, "stage")
		if stage == "" {
			return nil
		}
		if err := setStageStateTx(ctx, tx, runID, stage, "running", now, nil); err != nil {
			return err
		}
		return bumpWorkflowForRunTx(ctx, tx, runID, "running", now)
	case "stage_artifact":
		stage := eventString(payload, "stage")
		if stage == "" {
			return nil
		}
		if err := setStageStateTx(ctx, tx, runID, stage, "succeeded", now, nil); err != nil {
			return err
		}
		return bumpWorkflowForRunTx(ctx, tx, runID, "running", now)
	case "run_failed":
		stage := eventString(payload, "stage")
		code := eventString(payload, "code")
		if stage != "" {
			var codePtr *string
			if code != "" {
				codePtr = &code
			}
			if err := setStageStateTx(ctx, tx, runID, stage, "failed", now, codePtr); err != nil {
				return err
			}
		}
		return bumpWorkflowForRunTx(ctx, tx, runID, "failed", now)
	case "run_completed":
		return bumpWorkflowForRunTx(ctx, tx, runID, "completed", now)
	default:
		return nil
	}
}

func runWorkflowStatus(runStatus string) string {
	switch runStatus {
	case "queued", "running":
		return "running"
	case "succeeded":
		return "completed"
	case "failed":
		return "failed"
	case "interrupted":
		return "cancelled"
	default:
		return "recovering"
	}
}

// loadWorkflowSnapshotTx reads one comparable snapshot. On pre-FLO-74 data it
// performs an evidence-based, idempotent backfill before reading.
func (s *Store) loadWorkflowSnapshotTx(ctx context.Context, tx *sql.Tx, d *ProjectDetail) error {
	state, err := loadProjectWorkflowTx(ctx, tx, d.ID)
	if err != nil {
		if !isNoRows(err) {
			return err
		}
		state, err = s.recoverWorkflowTx(ctx, tx, d)
		if err != nil {
			return err
		}
	}

	stages, err := loadLatestStagesTx(ctx, tx, state.runID)
	if err != nil {
		return err
	}
	previewRunID, err := previewWorkflowRunTx(ctx, tx, d.StableVersionID)
	if err != nil {
		return err
	}
	conflicts, err := s.reconcileConsistencyTx(ctx, tx, d, &state, stages, previewRunID)
	if err != nil {
		return err
	}
	refreshed, err := loadProjectWorkflowTx(ctx, tx, d.ID)
	if err != nil {
		return err
	}
	state = refreshed

	d.WorkflowStatus = state.status
	d.StateVersion = state.version
	d.StateUpdatedAt = state.updatedAt
	d.ResponseUpdatedAt = s.nowTS()
	d.Stages = stages
	d.Consistency = WorkflowConsistency{OK: len(conflicts) == 0, ConflictCodes: conflicts}
	if state.runID.Valid {
		v := state.runID.String
		d.WorkflowRunID = &v
	}
	d.Preview = WorkflowPreview{Version: d.StableVersionID, WorkflowRunID: previewRunID}

	var latest *Run
	for i := range d.Runs {
		if state.runID.Valid && d.Runs[i].ID == state.runID.String {
			d.Runs[i].Stages = stages
			d.Runs[i].StageArtifacts = artifactsForRunAttempt(d.Artifacts, d.Runs[i].ID, stages)
			latest = &d.Runs[i]
			break
		}
	}
	if latest == nil && len(d.Runs) > 0 {
		latest = &d.Runs[len(d.Runs)-1]
	}
	d.LatestRun = latest
	if latest != nil && (latest.Status == "queued" || latest.Status == "running") {
		d.ActiveRun = latest
	}
	return nil
}

func loadProjectWorkflowTx(ctx context.Context, tx *sql.Tx, projectID string) (workflowProjectState, error) {
	var state workflowProjectState
	err := tx.QueryRowContext(ctx,
		`SELECT workflow_run_id, status, state_version, updated_at
		   FROM project_workflow_states WHERE project_id = ?`, projectID).
		Scan(&state.runID, &state.status, &state.version, &state.updatedAt)
	if err != nil {
		if isNoRows(err) {
			return state, sql.ErrNoRows
		}
		return state, fmt.Errorf("load project workflow state: %w", err)
	}
	return state, nil
}

func loadLatestStagesTx(ctx context.Context, tx *sql.Tx, runID sql.NullString) ([]WorkflowStageState, error) {
	if !runID.Valid {
		return nil, nil
	}
	var attempt int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(attempt), 0) FROM workflow_stage_runs WHERE workflow_run_id = ?`,
		runID.String).Scan(&attempt); err != nil {
		return nil, fmt.Errorf("load latest stage attempt: %w", err)
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT stage_key, status, attempt, attempt_id, started_at, finished_at, updated_at, error_code
		   FROM workflow_stage_runs
		  WHERE workflow_run_id = ? AND attempt = ?
		  ORDER BY CASE stage_key WHEN 'pm' THEN 1 WHEN 'architect' THEN 2 WHEN 'engineer' THEN 3 ELSE 4 END`,
		runID.String, attempt)
	if err != nil {
		return nil, fmt.Errorf("load workflow stages: %w", err)
	}
	defer rows.Close()
	var stages []WorkflowStageState
	for rows.Next() {
		var stage WorkflowStageState
		var attemptID, started, finished, code sql.NullString
		if err := rows.Scan(
			&stage.StageKey, &stage.Status, &stage.Attempt, &attemptID,
			&started, &finished, &stage.UpdatedAt, &code,
		); err != nil {
			return nil, fmt.Errorf("scan workflow stage: %w", err)
		}
		stage.Stage = stage.StageKey
		stage.AttemptID = ptrNullString(attemptID)
		stage.StartedAt = ptrNullString(started)
		stage.FinishedAt = ptrNullString(finished)
		stage.CompletedAt = stage.FinishedAt
		stage.ErrorCode = ptrNullString(code)
		stages = append(stages, stage)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workflow stages: %w", err)
	}
	return stages, nil
}

func ptrNullString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	out := v.String
	return &out
}

func artifactsForRunAttempt(all []StageArtifact, runID string, stages []WorkflowStageState) []StageArtifact {
	attemptID := ""
	if len(stages) > 0 && stages[0].AttemptID != nil {
		attemptID = *stages[0].AttemptID
	}
	var out []StageArtifact
	for _, a := range all {
		if a.RunID == runID && (attemptID == "" || a.AttemptID == attemptID) {
			out = append(out, a)
		}
	}
	for i := range stages {
		for _, a := range out {
			if a.Stage == stages[i].StageKey {
				artifactType, artifactRef := a.ArtifactType, a.ArtifactRef
				stages[i].ArtifactType = &artifactType
				stages[i].ArtifactRef = &artifactRef
			}
		}
	}
	return out
}

func previewWorkflowRunTx(ctx context.Context, tx *sql.Tx, versionID *string) (*string, error) {
	if versionID == nil {
		return nil, nil
	}

	// A version belongs to exactly one creator iteration through
	// versions.iteration_id. Manual iterations intentionally have no run of their
	// own, so their preview provenance is the workflow that produced their base
	// version. Follow that lineage until the nearest agent-created version.
	//
	// Do not look up iterations by result_version_id here: restore iterations can
	// point at an existing version and would otherwise shadow its real creator.
	currentVersionID := *versionID
	seen := make(map[string]struct{})
	for currentVersionID != "" {
		if _, exists := seen[currentVersionID]; exists {
			// Corrupt lineage remains observable as a nil provenance and is turned
			// into PREVIEW_WORKFLOW_MISMATCH by the consistency reconciler.
			return nil, nil
		}
		seen[currentVersionID] = struct{}{}

		var runID, baseVersionID sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT i.run_id, i.base_version_id
			   FROM versions v
			   JOIN iterations i ON i.id = v.iteration_id
			  WHERE v.id = ?`,
			currentVersionID).Scan(&runID, &baseVersionID)
		if err != nil {
			if isNoRows(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("load preview workflow relation: %w", err)
		}
		if runID.Valid {
			return ptrNullString(runID), nil
		}
		if !baseVersionID.Valid {
			return nil, nil
		}
		currentVersionID = baseVersionID.String
	}
	return nil, nil
}

func (s *Store) recoverWorkflowTx(ctx context.Context, tx *sql.Tx, d *ProjectDetail) (workflowProjectState, error) {
	now := s.nowTS()
	var run Run
	var base, active sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT id, project_id, status, prompt, base_version_id, active_attempt_id, created_at, updated_at
		   FROM runs WHERE project_id = ? ORDER BY updated_at DESC, created_at DESC LIMIT 1`, d.ID).
		Scan(&run.ID, &run.ProjectID, &run.Status, &run.Prompt, &base, &active, &run.CreatedAt, &run.UpdatedAt)
	if err != nil {
		if !isNoRows(err) {
			return workflowProjectState{}, fmt.Errorf("find historical workflow: %w", err)
		}
		status := "draft"
		if d.StableVersionID != nil {
			status = "recovering"
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO project_workflow_states(project_id, workflow_run_id, status, state_version, updated_at)
			 VALUES(?, NULL, ?, 1, ?)`, d.ID, status, now); err != nil {
			return workflowProjectState{}, fmt.Errorf("backfill no-run workflow state: %w", err)
		}
		return loadProjectWorkflowTx(ctx, tx, d.ID)
	}

	var attemptID string
	var attempt int
	err = tx.QueryRowContext(ctx,
		`SELECT id, sequence FROM attempts WHERE run_id = ? ORDER BY sequence DESC LIMIT 1`, run.ID).
		Scan(&attemptID, &attempt)
	if err != nil && !isNoRows(err) {
		return workflowProjectState{}, fmt.Errorf("find historical attempt: %w", err)
	}
	if isNoRows(err) {
		attempt = 0
	}

	evidence, failureStage, failureCode, err := loadHistoricalEvidenceTx(ctx, tx, run.ID, attemptID)
	if err != nil {
		return workflowProjectState{}, err
	}
	projectStatus := runWorkflowStatus(run.Status)
	for _, stageKey := range contracts.Load().Stages.Order {
		ev := evidence[stageKey]
		status := "waiting"
		if ev.artifactAt != "" {
			status = "succeeded"
		} else if ev.startedAt != "" {
			status = "running"
		}
		if run.Status == "succeeded" && status != "succeeded" {
			status = "recovering"
			projectStatus = "recovering"
		}
		if run.Status == "failed" && failureStage == stageKey {
			status = "failed"
		}
		if run.Status == "interrupted" && status == "running" {
			status = "cancelled"
		}
		var started, finished, code any
		if ev.startedAt != "" {
			started = ev.startedAt
		}
		if status == "succeeded" {
			finished = ev.artifactAt
		}
		if status == "failed" || status == "cancelled" {
			finished = run.UpdatedAt
		}
		if status == "failed" && failureCode != "" {
			code = failureCode
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO workflow_stage_runs(
			   workflow_run_id, attempt, attempt_id, stage_key, status,
			   started_at, finished_at, updated_at, error_code)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			run.ID, attempt, nullableAttempt(attemptID), stageKey, status,
			started, finished, run.UpdatedAt, code); err != nil {
			return workflowProjectState{}, fmt.Errorf("backfill stage %s: %w", stageKey, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO project_workflow_states(project_id, workflow_run_id, status, state_version, updated_at)
		 VALUES(?, ?, ?, 1, ?)`,
		d.ID, run.ID, projectStatus, now); err != nil {
		return workflowProjectState{}, fmt.Errorf("backfill project workflow: %w", err)
	}
	return loadProjectWorkflowTx(ctx, tx, d.ID)
}

type historicalStageEvidence struct {
	startedAt  string
	artifactAt string
}

func loadHistoricalEvidenceTx(
	ctx context.Context,
	tx *sql.Tx,
	runID, attemptID string,
) (map[string]historicalStageEvidence, string, string, error) {
	evidence := make(map[string]historicalStageEvidence)
	rows, err := tx.QueryContext(ctx,
		`SELECT type, payload, created_at FROM events WHERE run_id = ? ORDER BY seq ASC`, runID)
	if err != nil {
		return nil, "", "", fmt.Errorf("load recovery events: %w", err)
	}
	var failureStage, failureCode string
	for rows.Next() {
		var eventType, raw, createdAt string
		if err := rows.Scan(&eventType, &raw, &createdAt); err != nil {
			rows.Close()
			return nil, "", "", fmt.Errorf("scan recovery event: %w", err)
		}
		var payload map[string]any
		if json.Unmarshal([]byte(raw), &payload) != nil {
			continue
		}
		stage := eventString(payload, "stage")
		switch eventType {
		case "stage_started":
			ev := evidence[stage]
			ev.startedAt = createdAt
			evidence[stage] = ev
		case "run_failed":
			failureStage = stage
			failureCode = eventString(payload, "code")
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, "", "", fmt.Errorf("iterate recovery events: %w", err)
	}
	rows.Close()

	artifactRows, err := tx.QueryContext(ctx,
		`SELECT stage, created_at
		   FROM stage_artifacts
		  WHERE run_id = ? AND (? = '' OR attempt_id = ?)
		  ORDER BY created_at ASC`, runID, attemptID, attemptID)
	if err != nil {
		return nil, "", "", fmt.Errorf("load recovery artifacts: %w", err)
	}
	defer artifactRows.Close()
	for artifactRows.Next() {
		var stage, createdAt string
		if err := artifactRows.Scan(&stage, &createdAt); err != nil {
			return nil, "", "", fmt.Errorf("scan recovery artifact: %w", err)
		}
		ev := evidence[stage]
		ev.artifactAt = createdAt
		evidence[stage] = ev
	}
	if err := artifactRows.Err(); err != nil {
		return nil, "", "", fmt.Errorf("iterate recovery artifacts: %w", err)
	}
	return evidence, failureStage, failureCode, nil
}

func nullableAttempt(attemptID string) any {
	if attemptID == "" {
		return nil
	}
	return attemptID
}

func (s *Store) reconcileConsistencyTx(
	ctx context.Context,
	tx *sql.Tx,
	d *ProjectDetail,
	state *workflowProjectState,
	stages []WorkflowStageState,
	previewRunID *string,
) ([]string, error) {
	var runStatus string
	if state.runID.Valid {
		if err := tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE id = ?`, state.runID.String).Scan(&runStatus); err != nil {
			return nil, fmt.Errorf("load consistency run: %w", err)
		}
	}
	if runStatus != "succeeded" {
		return openConflictCodesTx(ctx, tx, d.ID)
	}

	var conflicts []string
	if d.StableVersionID == nil {
		conflicts = append(conflicts, "COMPLETED_WITHOUT_STABLE_PREVIEW")
	}
	if previewRunID == nil || !state.runID.Valid || *previewRunID != state.runID.String {
		conflicts = append(conflicts, "PREVIEW_WORKFLOW_MISMATCH")
	}
	if len(stages) != len(contracts.Load().Stages.Order) {
		conflicts = append(conflicts, "REQUIRED_STAGE_MISSING")
	} else {
		for _, stage := range stages {
			if stage.Status != "succeeded" {
				conflicts = append(conflicts, "REQUIRED_STAGE_NOT_SUCCEEDED")
				break
			}
			if stage.ArtifactRef == nil {
				// Artifact refs are hydrated after this validation. Verify in SQL.
				var count int
				if err := tx.QueryRowContext(ctx,
					`SELECT COUNT(*) FROM stage_artifacts
					  WHERE run_id = ? AND stage = ? AND (? IS NULL OR attempt_id = ?)`,
					state.runID.String, stage.StageKey, nullString(stage.AttemptID), nullString(stage.AttemptID)).
					Scan(&count); err != nil {
					return nil, fmt.Errorf("validate stage artifact: %w", err)
				}
				if count == 0 {
					conflicts = append(conflicts, "SUCCEEDED_STAGE_WITHOUT_ARTIFACT")
					break
				}
			}
		}
	}

	now := s.nowTS()
	if len(conflicts) > 0 {
		details, _ := json.Marshal(map[string]any{
			"projectId": d.ID, "workflowRunId": ptrNullString(state.runID),
			"stateVersion": state.version, "conflicts": conflicts,
		})
		for _, code := range conflicts {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO workflow_state_conflicts(
				   id, project_id, workflow_run_id, conflict_code, details, first_detected_at, resolved_at)
				 VALUES(?, ?, ?, ?, ?, ?, NULL)
				 ON CONFLICT(project_id, workflow_run_id, conflict_code) DO UPDATE SET
				   details = excluded.details, resolved_at = NULL`,
				newID(), d.ID, state.runID, code, string(details), now); err != nil {
				return nil, fmt.Errorf("record workflow conflict: %w", err)
			}
		}
		if state.status != "recovering" {
			if err := bumpWorkflowForRunTx(ctx, tx, state.runID.String, "recovering", now); err != nil {
				return nil, err
			}
		}
		return uniqueStrings(conflicts), nil
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE workflow_state_conflicts SET resolved_at = ?
		  WHERE project_id = ? AND resolved_at IS NULL`, now, d.ID); err != nil {
		return nil, fmt.Errorf("resolve workflow conflicts: %w", err)
	}
	if state.status == "recovering" {
		if err := bumpWorkflowForRunTx(ctx, tx, state.runID.String, "completed", now); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func openConflictCodesTx(ctx context.Context, tx *sql.Tx, projectID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT conflict_code FROM workflow_state_conflicts
		  WHERE project_id = ? AND resolved_at IS NULL ORDER BY conflict_code`, projectID)
	if err != nil {
		return nil, fmt.Errorf("load workflow conflicts: %w", err)
	}
	defer rows.Close()
	var codes []string
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, fmt.Errorf("scan workflow conflict: %w", err)
		}
		codes = append(codes, code)
	}
	return codes, rows.Err()
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
