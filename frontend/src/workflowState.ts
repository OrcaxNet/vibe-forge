import {
  STAGES,
  WORKFLOW_PROJECT_STATES,
  WORKFLOW_STAGE_STATES,
  type Stage,
  type WorkflowProjectStatus,
  type WorkflowStageStatus,
} from "./contract";

type UnknownRecord = Record<string, unknown>;

export type WorkflowStageView = {
  stage: Stage;
  status: WorkflowStageStatus;
  artifactRef?: string;
  artifactType?: string;
  startedAt?: string;
  completedAt?: string;
  integrityIssue?: string;
};

export type WorkflowSnapshot = {
  status: WorkflowProjectStatus;
  workflowRunId?: string;
  stateVersion: number;
  stateUpdatedAt: string;
  responseUpdatedAt: string;
  stages: WorkflowStageView[];
  preview: {
    version?: string;
    workflowRunId?: string;
  };
  consistency: {
    ok: boolean;
    conflictCodes: string[];
  };
};

function isRecord(value: unknown): value is UnknownRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function normalizeStageStatus(value: unknown): WorkflowStageStatus {
  if (value === undefined || value === null || value === "pending") {
    return "waiting";
  }
  return typeof value === "string" && WORKFLOW_STAGE_STATES.includes(value)
    ? (value as WorkflowStageStatus)
    : "recovering";
}

export function emptyWorkflowStages(): WorkflowStageView[] {
  return STAGES.map((stage) => ({ stage, status: "waiting" }));
}

export function normalizeWorkflowStages(
  value: unknown,
  artifactsValue?: unknown,
): WorkflowStageView[] {
  const rawStages = Array.isArray(value) ? value.filter(isRecord) : [];
  const rawArtifacts = Array.isArray(artifactsValue)
    ? artifactsValue.filter(isRecord)
    : [];

  return STAGES.map((stage) => {
    const raw =
      rawStages.find(
        (candidate) =>
          candidate.stageKey === stage || candidate.stage === stage,
      ) ?? {};
    const artifact =
      rawArtifacts.find((candidate) => candidate.stage === stage) ?? {};
    const artifactRef =
      asString(raw.artifactRef) || asString(artifact.artifactRef) || undefined;
    let status = normalizeStageStatus(raw.status);
    let integrityIssue: string | undefined;

    if (status === "succeeded" && !artifactRef) {
      status = "recovering";
      integrityIssue = "阶段已标记完成，但制品记录尚未恢复。";
    }

    return {
      stage,
      status,
      artifactRef,
      artifactType:
        asString(raw.artifactType) ||
        asString(artifact.artifactType) ||
        undefined,
      startedAt: asString(raw.startedAt) || undefined,
      completedAt:
        asString(raw.completedAt) ||
        asString(raw.finishedAt) ||
        undefined,
      integrityIssue,
    };
  });
}

export function normalizeWorkflowSnapshot(
  value: unknown,
): WorkflowSnapshot | undefined {
  if (!isRecord(value)) return undefined;
  const stateVersion = Number(value.stateVersion);
  const requestedStatus = asString(value.workflowStatus);
  if (
    !Number.isSafeInteger(stateVersion) ||
    stateVersion < 1 ||
    !WORKFLOW_PROJECT_STATES.includes(requestedStatus)
  ) {
    return undefined;
  }

  const previewRaw = isRecord(value.preview) ? value.preview : {};
  const consistencyRaw = isRecord(value.consistency)
    ? value.consistency
    : {};
  const workflowRunId = asString(value.workflowRunId) || undefined;
  const preview = {
    version: asString(previewRaw.version) || undefined,
    workflowRunId: asString(previewRaw.workflowRunId) || undefined,
  };
  const conflictCodes = Array.isArray(consistencyRaw.conflictCodes)
    ? consistencyRaw.conflictCodes
        .filter((code): code is string => typeof code === "string")
        .filter(Boolean)
    : [];
  const consistency = {
    ok: consistencyRaw.ok === true && conflictCodes.length === 0,
    conflictCodes,
  };
  let stages = normalizeWorkflowStages(value.stages);
  const completedConflict =
    requestedStatus === "completed" &&
    (!consistency.ok ||
      !preview.version ||
      !workflowRunId ||
      preview.workflowRunId !== workflowRunId ||
      stages.some((stage) => stage.status !== "succeeded"));
  if (completedConflict) {
    stages = stages.map((stage) =>
      stage.status === "succeeded"
        ? stage
        : {
            ...stage,
            status: "recovering",
            integrityIssue:
              stage.integrityIssue ?? "完成记录与阶段证据不一致，正在核对。",
          },
    );
  }

  return {
    status: completedConflict
      ? "recovering"
      : (requestedStatus as WorkflowProjectStatus),
    workflowRunId,
    stateVersion,
    stateUpdatedAt: asString(value.stateUpdatedAt),
    responseUpdatedAt: asString(value.responseUpdatedAt),
    stages,
    preview,
    consistency: completedConflict
      ? {
          ok: false,
          conflictCodes:
            conflictCodes.length > 0
              ? conflictCodes
              : ["CLIENT_COMPLETED_SNAPSHOT_CONFLICT"],
        }
      : consistency,
  };
}

export function shouldApplyWorkflowSnapshot({
  versionFloor,
  incomingVersion,
  localRevisionAtRequest,
  currentLocalRevision,
}: {
  versionFloor: number;
  incomingVersion: number;
  localRevisionAtRequest: number;
  currentLocalRevision: number;
}): boolean {
  if (incomingVersion < versionFloor) return false;
  if (
    incomingVersion === versionFloor &&
    localRevisionAtRequest !== currentLocalRevision
  ) {
    return false;
  }
  return true;
}

export function shouldPollWorkflow(
  status: WorkflowProjectStatus | undefined,
): boolean {
  return status === "running" || status === "recovering";
}

export function workflowLoadFailureMode(
  hasTrustedSnapshot: boolean,
): "preserve_snapshot" | "show_error" {
  return hasTrustedSnapshot ? "preserve_snapshot" : "show_error";
}
