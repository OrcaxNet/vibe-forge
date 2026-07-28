import { describe, expect, it } from "vitest";
import {
  normalizeWorkflowSnapshot,
  shouldApplyWorkflowSnapshot,
  shouldPollWorkflow,
  workflowLoadFailureMode,
} from "./workflowState";

const completedSnapshot = {
  workflowStatus: "completed",
  workflowRunId: "run-2",
  stateVersion: 12,
  stateUpdatedAt: "2026-07-28T10:00:00Z",
  responseUpdatedAt: "2026-07-28T10:00:01Z",
  stages: ["pm", "architect", "engineer", "qa"].map((stage) => ({
    stageKey: stage,
    status: "succeeded",
    artifactRef: `${stage}-artifact`,
  })),
  preview: { version: "version-2", workflowRunId: "run-2" },
  consistency: { ok: true, conflictCodes: [] },
};

describe("durable workflow snapshots", () => {
  it("restores all completed stages from the server snapshot", () => {
    const snapshot = normalizeWorkflowSnapshot(completedSnapshot);
    expect(snapshot?.status).toBe("completed");
    expect(snapshot?.stateVersion).toBe(12);
    expect(snapshot?.stages.map((stage) => stage.status)).toEqual([
      "succeeded",
      "succeeded",
      "succeeded",
      "succeeded",
    ]);
  });

  it("turns a contradictory completed snapshot into an explicit recovery state", () => {
    const snapshot = normalizeWorkflowSnapshot({
      ...completedSnapshot,
      stages: completedSnapshot.stages.map((stage) =>
        stage.stageKey === "qa" ? { ...stage, status: "waiting" } : stage,
      ),
    });
    expect(snapshot?.status).toBe("recovering");
    expect(snapshot?.consistency.ok).toBe(false);
    expect(snapshot?.consistency.conflictCodes).toContain(
      "CLIENT_COMPLETED_SNAPSHOT_CONFLICT",
    );
    expect(snapshot?.stages.at(-1)?.status).toBe("recovering");
  });

  it("shows server-confirmed waiting only for a draft without run evidence", () => {
    const snapshot = normalizeWorkflowSnapshot({
      workflowStatus: "draft",
      stateVersion: 1,
      stateUpdatedAt: "2026-07-28T10:00:00Z",
      responseUpdatedAt: "2026-07-28T10:00:01Z",
      stages: [],
      preview: {},
      consistency: { ok: true, conflictCodes: [] },
    });
    expect(snapshot?.status).toBe("draft");
    expect(snapshot?.stages.every((stage) => stage.status === "waiting")).toBe(
      true,
    );
  });

  it("does not apply a late response below the current version floor", () => {
    expect(
      shouldApplyWorkflowSnapshot({
        versionFloor: 15,
        incomingVersion: 14,
        localRevisionAtRequest: 3,
        currentLocalRevision: 3,
      }),
    ).toBe(false);
  });

  it("does not let an equal-version response erase a newer local event", () => {
    expect(
      shouldApplyWorkflowSnapshot({
        versionFloor: 15,
        incomingVersion: 15,
        localRevisionAtRequest: 3,
        currentLocalRevision: 4,
      }),
    ).toBe(false);
    expect(
      shouldApplyWorkflowSnapshot({
        versionFloor: 15,
        incomingVersion: 16,
        localRevisionAtRequest: 3,
        currentLocalRevision: 4,
      }),
    ).toBe(true);
  });

  it("polls only non-terminal server states", () => {
    expect(shouldPollWorkflow("running")).toBe(true);
    expect(shouldPollWorkflow("recovering")).toBe(true);
    expect(shouldPollWorkflow("completed")).toBe(false);
    expect(shouldPollWorkflow("failed")).toBe(false);
  });

  it("preserves a trusted snapshot on refresh failure", () => {
    expect(workflowLoadFailureMode(true)).toBe("preserve_snapshot");
    expect(workflowLoadFailureMode(false)).toBe("show_error");
  });
});
