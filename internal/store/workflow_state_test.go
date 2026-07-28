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
