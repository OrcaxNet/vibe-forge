// Frontend view of the Vibe Forge shared contract. This module imports the
// canonical contracts/contract.json (the SAME file the Go backend embeds) and
// exposes typed constants. Never hand-copy values from the contract into other
// frontend files; import them from here so the frontend cannot drift from the
// backend.
import contract from "../../contracts/contract.json";

export const contractVersion: string = contract.version;

export const STAGES = contract.stages.order as readonly string[];
export type Stage = (typeof contract.stages.order)[number];

export const PROJECT_STATES = contract.states.project.values as readonly string[];
export const RUN_STATES = contract.states.run.values as readonly string[];
export const VERSION_STATES = contract.states.version.values as readonly string[];
export const STAGE_NODE_STATES = contract.states.stageNode.values as readonly string[];
export const WORKFLOW_PROJECT_STATES =
  contract.states.workflowProject.values as readonly string[];
export const WORKFLOW_STAGE_STATES =
  contract.states.workflowStage.values as readonly string[];

export type ProjectStatus = (typeof PROJECT_STATES)[number];
export type RunStatus = (typeof RUN_STATES)[number];
export type VersionStatus = (typeof VERSION_STATES)[number];
export type StageNodeStatus = (typeof STAGE_NODE_STATES)[number];
export type WorkflowProjectStatus = (typeof WORKFLOW_PROJECT_STATES)[number];
export type WorkflowStageStatus = (typeof WORKFLOW_STAGE_STATES)[number];

/** The 8 unified SSE event names (PRD-A/B/C §5.2). */
export const EVENT_NAMES = Object.keys(contract.events.definitions) as readonly string[];
export type EventName = (typeof EVENT_NAMES)[number];

export const IDEMPOTENCY_HEADER: string = contract.idempotency.header;
export const IDEMPOTENCY_TTL_SECONDS: number = contract.idempotency.ttlSeconds;

/** The only writable business file in the fixed scaffold (PRD-B §5.3). */
export const WRITABLE_FILE_PATH: string = contract.limits.writableFilePath;
export const PROMPT_MAX_CHARS: number = contract.limits.promptMaxChars;
export const PROMPT_MIN_CHARS: number = contract.limits.promptMinChars;
export const SANDPACK_READY_TIMEOUT_MS: number =
  contract.limits.sandpackReadyTimeoutSeconds * 1_000;

/** REST paths keyed by contract name (PRD-A/B/C §5.1). */
export const PATHS = contract.paths as Record<string, {
  method: string;
  path: string;
  type?: string;
}>;

/** Stable error codes -> HTTP status (contract §errors). */
export const ERROR_CODES = contract.errors.codes as Record<string, {
  http: number;
  retryable: boolean;
}>;

export type ApiError = {
  code: string;
  message: string;
  retryable: boolean;
  details?: Record<string, unknown>;
};

export default contract;
