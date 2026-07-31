package store

import (
	"testing"
)

func activeAttemptID(t *testing.T, s *Store, runID string) string {
	t.Helper()
	var attemptID string
	if err := s.db.QueryRow(
		`SELECT active_attempt_id FROM runs WHERE id = ?`, runID,
	).Scan(&attemptID); err != nil {
		t.Fatalf("read active attempt: %v", err)
	}
	return attemptID
}

func stageByKey(t *testing.T, stages []WorkflowStageState, key string) WorkflowStageState {
	t.Helper()
	for _, stage := range stages {
		if stage.StageKey == key {
			return stage
		}
	}
	t.Fatalf("stage %q missing from %#v", key, stages)
	return WorkflowStageState{}
}

func TestWorkflowSnapshotPreservesTerminalAndActiveSemantics(t *testing.T) {
	tests := []struct {
		name          string
		drive         func(*testing.T, *Store, string, string)
		wantWorkflow  string
		wantPM        string
		wantErrorCode string
	}{
		{
			name: "running",
			drive: func(t *testing.T, s *Store, runID, attemptID string) {
				if err := s.BeginAttempt(ctx, runID, attemptID); err != nil {
					t.Fatal(err)
				}
				if _, err := s.AppendEvent(ctx, runID, "stage_started", map[string]any{
					"runId": runID, "stage": "pm", "sequence": 1,
				}); err != nil {
					t.Fatal(err)
				}
			},
			wantWorkflow: "running",
			wantPM:       "running",
		},
		{
			name: "failed",
			drive: func(t *testing.T, s *Store, runID, attemptID string) {
				if err := s.BeginAttempt(ctx, runID, attemptID); err != nil {
					t.Fatal(err)
				}
				if _, err := s.AppendEvent(ctx, runID, "stage_started", map[string]any{
					"runId": runID, "stage": "pm", "sequence": 1,
				}); err != nil {
					t.Fatal(err)
				}
				if err := s.FailRun(ctx, runID); err != nil {
					t.Fatal(err)
				}
				if _, err := s.AppendEvent(ctx, runID, "run_failed", map[string]any{
					"runId": runID, "stage": "pm", "code": "UPSTREAM_ERROR", "retryable": true,
				}); err != nil {
					t.Fatal(err)
				}
			},
			wantWorkflow:  "failed",
			wantPM:        "failed",
			wantErrorCode: "UPSTREAM_ERROR",
		},
		{
			name: "cancelled after restart reconciliation",
			drive: func(t *testing.T, s *Store, runID, attemptID string) {
				if err := s.BeginAttempt(ctx, runID, attemptID); err != nil {
					t.Fatal(err)
				}
				if _, err := s.AppendEvent(ctx, runID, "stage_started", map[string]any{
					"runId": runID, "stage": "pm", "sequence": 1,
				}); err != nil {
					t.Fatal(err)
				}
				if n, err := s.MarkActiveRunsInterrupted(ctx); err != nil || n != 1 {
					t.Fatalf("MarkActiveRunsInterrupted = %d, %v; want 1, nil", n, err)
				}
			},
			wantWorkflow: "cancelled",
			wantPM:       "cancelled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			p := createProject(t, s, "project-"+tt.name, "build an app")
			runID, _ := createRun(t, s, p.ID, "build an app", "", "run-"+tt.name)
			attemptID := activeAttemptID(t, s, runID)
			tt.drive(t, s, runID, attemptID)

			detail, err := s.GetProject(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if detail.WorkflowStatus != tt.wantWorkflow {
				t.Errorf("workflow status = %q, want %q", detail.WorkflowStatus, tt.wantWorkflow)
			}
			pm := stageByKey(t, detail.Stages, "pm")
			if pm.Status != tt.wantPM {
				t.Errorf("pm status = %q, want %q", pm.Status, tt.wantPM)
			}
			if tt.wantErrorCode != "" && (pm.ErrorCode == nil || *pm.ErrorCode != tt.wantErrorCode) {
				t.Errorf("pm errorCode = %v, want %q", pm.ErrorCode, tt.wantErrorCode)
			}
			if detail.StateVersion <= 1 || detail.StateUpdatedAt == "" || detail.ResponseUpdatedAt == "" {
				t.Errorf("invalid comparable snapshot: version=%d stateAt=%q responseAt=%q",
					detail.StateVersion, detail.StateUpdatedAt, detail.ResponseUpdatedAt)
			}
		})
	}
}

func TestWorkflowCompletedSnapshotSurvivesReload(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "project-completed", "build an app")
	runID, iterationID := createRun(t, s, p.ID, "build an app", "", "run-completed")
	attemptID := activeAttemptID(t, s, runID)
	if err := s.BeginAttempt(ctx, runID, attemptID); err != nil {
		t.Fatal(err)
	}
	for i, stage := range []string{"pm", "architect", "engineer", "qa"} {
		if _, err := s.AppendEvent(ctx, runID, "stage_started", map[string]any{
			"runId": runID, "stage": stage, "sequence": i + 1,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.RecordStageArtifact(ctx, runID, attemptID, stage, stage+"_artifact", "ref:"+stage); err != nil {
			t.Fatal(err)
		}
	}
	version := commitStable(t, s, p.ID, iterationID, runID, sampleFiles("completed"))

	first, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.WorkflowStatus != "completed" || !first.Consistency.OK {
		t.Fatalf("completed snapshot = status %q consistency %#v", first.WorkflowStatus, first.Consistency)
	}
	if first.Preview.Version == nil || *first.Preview.Version != version.ID ||
		first.Preview.WorkflowRunID == nil || *first.Preview.WorkflowRunID != runID {
		t.Errorf("preview relation = %#v, want version %q run %q", first.Preview, version.ID, runID)
	}
	for _, stage := range first.Stages {
		if stage.Status != "succeeded" || stage.ArtifactRef == nil || stage.FinishedAt == nil {
			t.Errorf("completed stage not durable: %#v", stage)
		}
	}
	if second.StateVersion != first.StateVersion {
		t.Errorf("read-only reload changed stateVersion: %d -> %d", first.StateVersion, second.StateVersion)
	}
}

func seedCompletedWorkflow(t *testing.T, s *Store, key string) (Project, string, string) {
	t.Helper()

	p := createProject(t, s, "project-"+key, "build an app")
	runID, iterationID := createRun(t, s, p.ID, "build an app", "", "run-"+key)
	attemptID := activeAttemptID(t, s, runID)
	if err := s.BeginAttempt(ctx, runID, attemptID); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"pm", "architect", "engineer", "qa"} {
		if _, err := s.RecordStageArtifact(
			ctx, runID, attemptID, stage, stage+"_artifact", "ref:"+stage,
		); err != nil {
			t.Fatal(err)
		}
	}
	version, err := s.CommitVersion(ctx, CommitInput{
		ProjectID: p.ID, IterationID: iterationID, RunID: runID, AttemptID: attemptID,
		Files: sampleFiles("completed"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return p, runID, version.ID
}

func TestManualSavePreservesCompletedWorkflowProvenance(t *testing.T) {
	s, _ := newTestStore(t)
	p, runID, stable := seedCompletedWorkflow(t, s, "manual-provenance")

	before, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.WorkflowStatus != "completed" || !before.Consistency.OK {
		t.Fatalf("initial workflow = status %q consistency %#v", before.WorkflowStatus, before.Consistency)
	}

	bad := `export default function App() { return <main><h1>broken</main>; }`
	status, body, _, err := s.WriteFile(ctx, p.ID, bad, stable, "manual-bad")
	if err != nil {
		t.Fatal(err)
	}
	if status != 422 {
		t.Fatalf("invalid manual save status = %d, want 422 (body=%s)", status, body)
	}
	afterFailure, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterFailure.StableVersionID == nil || *afterFailure.StableVersionID != stable {
		t.Fatalf("stable after invalid save = %v, want %q", afterFailure.StableVersionID, stable)
	}
	if afterFailure.WorkflowStatus != "completed" || !afterFailure.Consistency.OK {
		t.Fatalf("workflow after invalid save = status %q consistency %#v",
			afterFailure.WorkflowStatus, afterFailure.Consistency)
	}
	if afterFailure.StateVersion != before.StateVersion {
		t.Fatalf("invalid save changed stateVersion: %d -> %d",
			before.StateVersion, afterFailure.StateVersion)
	}

	good := `export default function App() { return <main><h1>fixed</h1></main>; }`
	status, body, _, err = s.WriteFile(ctx, p.ID, good, stable, "manual-good")
	if err != nil {
		t.Fatal(err)
	}
	if status != 202 {
		t.Fatalf("valid manual save status = %d, want 202 (body=%s)", status, body)
	}
	manualStable := stableVersionID(t, s, p.ID)
	if manualStable == stable {
		t.Fatal("valid manual save did not advance stableVersionId")
	}

	afterSuccess, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterSuccess.WorkflowStatus != "completed" || !afterSuccess.Consistency.OK {
		t.Fatalf("workflow after valid save = status %q consistency %#v",
			afterSuccess.WorkflowStatus, afterSuccess.Consistency)
	}
	if afterSuccess.Preview.Version == nil || *afterSuccess.Preview.Version != manualStable ||
		afterSuccess.Preview.WorkflowRunID == nil || *afterSuccess.Preview.WorkflowRunID != runID {
		t.Fatalf("manual preview provenance = %#v, want version %q run %q",
			afterSuccess.Preview, manualStable, runID)
	}
	if afterSuccess.StateVersion <= afterFailure.StateVersion {
		t.Fatalf("valid save stateVersion = %d, want greater than %d",
			afterSuccess.StateVersion, afterFailure.StateVersion)
	}

	reloaded, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.WorkflowStatus != "completed" || !reloaded.Consistency.OK {
		t.Fatalf("reloaded workflow = status %q consistency %#v",
			reloaded.WorkflowStatus, reloaded.Consistency)
	}
	if reloaded.StateVersion != afterSuccess.StateVersion {
		t.Fatalf("read-only reload changed stateVersion: %d -> %d",
			afterSuccess.StateVersion, reloaded.StateVersion)
	}
	var openConflicts int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM workflow_state_conflicts
		  WHERE project_id = ? AND resolved_at IS NULL`, p.ID,
	).Scan(&openConflicts); err != nil {
		t.Fatal(err)
	}
	if openConflicts != 0 {
		t.Fatalf("open workflow conflicts = %d, want 0", openConflicts)
	}

	status, _, _, err = s.CreateRun(
		ctx, p.ID, "make the title blue", manualStable, "next-natural-language-run", true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if status != 202 {
		t.Fatalf("natural-language edit after manual save status = %d, want 202", status)
	}
}

func TestManualPreviewMismatchRecoveryHealsExistingProject(t *testing.T) {
	s, _ := newTestStore(t)
	p, runID, stable := seedCompletedWorkflow(t, s, "manual-recovery")

	good := `export default function App() { return <main><h1>saved</h1></main>; }`
	status, body, _, err := s.WriteFile(ctx, p.ID, good, stable, "manual-recovery-save")
	if err != nil {
		t.Fatal(err)
	}
	if status != 202 {
		t.Fatalf("valid manual save status = %d, want 202 (body=%s)", status, body)
	}

	now := s.nowTS()
	if _, err := s.db.Exec(
		`UPDATE project_workflow_states
		    SET status = 'recovering', state_version = state_version + 1, updated_at = ?
		  WHERE project_id = ?`, now, p.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO workflow_state_conflicts(
		   id, project_id, workflow_run_id, conflict_code, details, first_detected_at, resolved_at)
		 VALUES(?, ?, ?, 'PREVIEW_WORKFLOW_MISMATCH', '{}', ?, NULL)`,
		newID(), p.ID, runID, now,
	); err != nil {
		t.Fatal(err)
	}

	detail, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.WorkflowStatus != "completed" || !detail.Consistency.OK {
		t.Fatalf("healed workflow = status %q consistency %#v",
			detail.WorkflowStatus, detail.Consistency)
	}
	if detail.Preview.WorkflowRunID == nil || *detail.Preview.WorkflowRunID != runID {
		t.Fatalf("healed preview workflowRunId = %v, want %q", detail.Preview.WorkflowRunID, runID)
	}
	var resolvedAt string
	if err := s.db.QueryRow(
		`SELECT resolved_at FROM workflow_state_conflicts
		  WHERE project_id = ? AND workflow_run_id = ?
		    AND conflict_code = 'PREVIEW_WORKFLOW_MISMATCH'`,
		p.ID, runID,
	).Scan(&resolvedAt); err != nil {
		t.Fatal(err)
	}
	if resolvedAt == "" {
		t.Fatal("existing PREVIEW_WORKFLOW_MISMATCH was not resolved")
	}
}

func TestHistoricalWorkflowRecoveryIsConservativeAndIdempotent(t *testing.T) {
	tests := []struct {
		name             string
		withAllArtifacts bool
		wantStatus       string
		wantConsistency  bool
	}{
		{name: "complete evidence", withAllArtifacts: true, wantStatus: "completed", wantConsistency: true},
		{name: "missing stage evidence", withAllArtifacts: false, wantStatus: "recovering", wantConsistency: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			p := createProject(t, s, "legacy-project-"+tt.name, "build an app")
			runID, iterationID := createRun(t, s, p.ID, "build an app", "", "legacy-run-"+tt.name)
			attemptID := activeAttemptID(t, s, runID)
			if err := s.BeginAttempt(ctx, runID, attemptID); err != nil {
				t.Fatal(err)
			}
			stages := []string{"pm", "architect", "engineer", "qa"}
			if !tt.withAllArtifacts {
				stages = stages[:3]
			}
			for _, stage := range stages {
				if _, err := s.RecordStageArtifact(ctx, runID, attemptID, stage, "artifact", "ref:"+stage); err != nil {
					t.Fatal(err)
				}
			}
			commitStable(t, s, p.ID, iterationID, runID, sampleFiles("legacy"))
			if _, err := s.db.Exec(`DELETE FROM project_workflow_states WHERE project_id = ?`, p.ID); err != nil {
				t.Fatal(err)
			}
			// Cascading is intentionally not used here: stage history represents
			// the new fields missing from a legacy database.
			if _, err := s.db.Exec(`DELETE FROM workflow_stage_runs WHERE workflow_run_id = ?`, runID); err != nil {
				t.Fatal(err)
			}

			first, err := s.GetProject(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			second, err := s.GetProject(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if first.WorkflowStatus != tt.wantStatus || first.Consistency.OK != tt.wantConsistency {
				t.Errorf("recovery = status %q consistency %#v, want %q/%v",
					first.WorkflowStatus, first.Consistency, tt.wantStatus, tt.wantConsistency)
			}
			if second.StateVersion != first.StateVersion {
				t.Errorf("idempotent recovery changed version: %d -> %d", first.StateVersion, second.StateVersion)
			}
			if !tt.withAllArtifacts && stageByKey(t, first.Stages, "qa").Status == "waiting" {
				t.Error("missing historical evidence silently fell back to waiting")
			}
		})
	}
}

func TestStateVersionOrdersConcurrentResponses(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "project-version", "build an app")
	runID, _ := createRun(t, s, p.ID, "build an app", "", "run-version")
	attemptID := activeAttemptID(t, s, runID)
	if err := s.BeginAttempt(ctx, runID, attemptID); err != nil {
		t.Fatal(err)
	}
	older, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendEvent(ctx, runID, "stage_started", map[string]any{
		"runId": runID, "stage": "pm", "sequence": 1,
	}); err != nil {
		t.Fatal(err)
	}
	newer, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if newer.StateVersion <= older.StateVersion {
		t.Fatalf("newer stateVersion %d is not greater than older %d", newer.StateVersion, older.StateVersion)
	}
}

func TestLateOlderRunUpdateCannotRepointProjectSnapshot(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "project-stale-run", "build an app")
	oldRunID, oldIterationID := createRun(t, s, p.ID, "build an app", "", "old-run")
	oldAttemptID := activeAttemptID(t, s, oldRunID)
	if err := s.BeginAttempt(ctx, oldRunID, oldAttemptID); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"pm", "architect", "engineer", "qa"} {
		if _, err := s.RecordStageArtifact(ctx, oldRunID, oldAttemptID, stage, "artifact", "ref:"+stage); err != nil {
			t.Fatal(err)
		}
	}
	version := commitStable(t, s, p.ID, oldIterationID, oldRunID, sampleFiles("v1"))
	newRunID, _ := createRun(t, s, p.ID, "edit the app", version.ID, "new-run")
	before, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetRunStatus(ctx, oldRunID, "failed"); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.WorkflowRunID == nil || *after.WorkflowRunID != newRunID {
		t.Fatalf("late old run repointed workflow to %v, want %q", after.WorkflowRunID, newRunID)
	}
	if after.StateVersion != before.StateVersion {
		t.Errorf("stale run changed current stateVersion: %d -> %d", before.StateVersion, after.StateVersion)
	}
}
