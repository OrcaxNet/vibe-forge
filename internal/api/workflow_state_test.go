package api

import (
	"context"
	"encoding/json"
	"testing"
)

func TestGetProjectReturnsComparableWorkflowSnapshot(t *testing.T) {
	srv := newAPITestServer(t)
	_, body := doJSON(t, srv, "POST", "/api/projects", "workflow-project",
		map[string]string{"initialPrompt": "build an app"})
	var project struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &project); err != nil {
		t.Fatal(err)
	}
	_, body = doJSON(t, srv, "POST", "/api/projects/"+project.ID+"/runs", "workflow-run",
		map[string]string{"prompt": "build an app"})
	var run struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(body, &run); err != nil {
		t.Fatal(err)
	}
	var attemptID string
	if err := srv.store.DB().QueryRow(
		`SELECT active_attempt_id FROM runs WHERE id = ?`, run.RunID,
	).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.BeginAttempt(context.Background(), run.RunID, attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.AppendEvent(context.Background(), run.RunID, "stage_started", map[string]any{
		"runId": run.RunID, "stage": "pm", "sequence": 1,
	}); err != nil {
		t.Fatal(err)
	}

	status, body := doJSON(t, srv, "GET", "/api/projects/"+project.ID, "", nil)
	if status != 200 {
		t.Fatalf("GET project status = %d, body=%s", status, body)
	}
	var snapshot struct {
		WorkflowStatus    string `json:"workflowStatus"`
		WorkflowRunID     string `json:"workflowRunId"`
		StateVersion      int64  `json:"stateVersion"`
		StateUpdatedAt    string `json:"stateUpdatedAt"`
		ResponseUpdatedAt string `json:"responseUpdatedAt"`
		Stages            []struct {
			StageKey string `json:"stageKey"`
			Status   string `json:"status"`
			Attempt  int    `json:"attempt"`
		} `json:"stages"`
		LatestRun struct {
			ID     string `json:"id"`
			Stages []struct {
				StageKey string `json:"stageKey"`
				Status   string `json:"status"`
			} `json:"stages"`
		} `json:"latestRun"`
	}
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.WorkflowStatus != "running" || snapshot.WorkflowRunID != run.RunID {
		t.Errorf("workflow identity/status = %q/%q, want running/%q",
			snapshot.WorkflowStatus, snapshot.WorkflowRunID, run.RunID)
	}
	if snapshot.StateVersion <= 1 || snapshot.StateUpdatedAt == "" || snapshot.ResponseUpdatedAt == "" {
		t.Errorf("snapshot is not comparable: %#v", snapshot)
	}
	if len(snapshot.Stages) != 4 || snapshot.Stages[0].StageKey != "pm" ||
		snapshot.Stages[0].Status != "running" || snapshot.Stages[0].Attempt != 1 {
		t.Errorf("stage snapshot = %#v", snapshot.Stages)
	}
	if snapshot.LatestRun.ID != run.RunID || len(snapshot.LatestRun.Stages) != 4 {
		t.Errorf("latestRun compatibility projection = %#v", snapshot.LatestRun)
	}
}
