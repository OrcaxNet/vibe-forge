import {
  FormEvent,
  KeyboardEvent,
  ReactNode,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import {
  EVENT_NAMES,
  IDEMPOTENCY_HEADER,
  PATHS,
  PROMPT_MAX_CHARS,
  PROMPT_MIN_CHARS,
  STAGES,
  type ApiError,
  type RunStatus,
  type Stage,
  type StageNodeStatus,
} from "./contract";

type JsonObject = Record<string, unknown>;

type ProjectSummary = {
  id: string;
  title: string;
  status: string;
  updatedAt: string;
  stableVersionId?: string;
};

type StageView = {
  stage: Stage;
  status: StageNodeStatus;
  artifactRef?: string;
  artifactType?: string;
  startedAt?: string;
  completedAt?: string;
  integrityIssue?: string;
};

type RunView = {
  id: string;
  status: RunStatus;
  activeAttemptId?: string;
  prompt?: string;
  failureCode?: string;
  failureMessage?: string;
  retryable?: boolean;
  stages: StageView[];
};

type Message = {
  id: string;
  role: "user" | "assistant";
  content: string;
  createdAt?: string;
};

type ProjectDetail = ProjectSummary & {
  messages: Message[];
  activeRun?: RunView;
  latestRun?: RunView;
};

type RunCreated = {
  runId: string;
  attemptId?: string;
};

type ConnectionState =
  | "idle"
  | "connecting"
  | "connected"
  | "reconnecting"
  | "stale";
type WorkspaceTab = "build" | "preview" | "files";

type Route = { kind: "home" } | { kind: "project"; projectId: string };

type Bootstrap = {
  project: ProjectSummary;
  prompt: string;
  run?: RunCreated;
  runError?: ApiClientError;
};

const STAGE_LABELS: Record<
  Stage,
  { title: string; role: string; artifact: string }
> = {
  pm: { title: "需求成形", role: "PM", artifact: "产品规格" },
  architect: { title: "结构搭建", role: "Architect", artifact: "结构方案" },
  engineer: { title: "代码锻造", role: "Engineer", artifact: "源文件" },
  qa: { title: "质量淬火", role: "QA", artifact: "编译结果" },
};

const EXAMPLE_PROMPTS = [
  "做一个能新增、完成和删除习惯的每日追踪器",
  "设计一张适合独立咖啡店的预约落地页",
  "创建一个带倒计时和分类筛选的学习计划板",
] as const;

const BUTTON_PRIMARY =
  "inline-flex min-h-11 items-center justify-center gap-2 rounded-xl bg-[#1756d8] px-5 py-3 text-sm font-bold text-white shadow-[0_8px_24px_rgba(23,86,216,0.24)] transition hover:bg-[#1248bb] disabled:cursor-not-allowed disabled:bg-[#9aa8bd] disabled:shadow-none focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-3 focus-visible:outline-[#1756d8]";
const BUTTON_SECONDARY =
  "inline-flex min-h-10 items-center justify-center gap-2 rounded-xl border border-[#cdd5df] bg-white px-4 py-2 text-sm font-semibold text-[#17243b] transition hover:border-[#93a2b6] hover:bg-[#f7f9fc] disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-3 focus-visible:outline-[#1756d8]";

class ApiClientError extends Error {
  readonly status: number;
  readonly code: string;
  readonly retryable: boolean;
  readonly details?: JsonObject;

  constructor(
    status: number,
    error: Partial<ApiError> & { details?: JsonObject },
  ) {
    super(error.message ?? "请求未完成，请重试。");
    this.name = "ApiClientError";
    this.status = status;
    this.code = error.code ?? "INTERNAL";
    this.retryable = error.retryable ?? status >= 500;
    this.details = error.details;
  }
}

function isObject(value: unknown): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function stringValue(value: unknown, fallback = ""): string {
  return typeof value === "string" ? value : fallback;
}

function asArray(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

function createIdempotencyKey(scope: string): string {
  const id =
    typeof crypto !== "undefined" && "randomUUID" in crypto
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(16).slice(2)}`;
  return `${scope}:${id}`;
}

function fillPath(template: string, values: Record<string, string>): string {
  return Object.entries(values).reduce(
    (path, [key, value]) => path.replace(`:${key}`, encodeURIComponent(value)),
    template,
  );
}

async function requestJson<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response;
  try {
    response = await fetch(path, init);
  } catch {
    throw new ApiClientError(0, {
      code: "CONNECTION_ERROR",
      message: "无法连接到构建服务。检查网络后重试。",
      retryable: true,
    });
  }

  const text = await response.text();
  let body: unknown = {};
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = {};
    }
  }

  if (!response.ok) {
    const error = isObject(body) ? body : {};
    throw new ApiClientError(response.status, {
      code: stringValue(error.code, `HTTP_${response.status}`),
      message: stringValue(error.message, "请求未完成，请重试。"),
      retryable:
        typeof error.retryable === "boolean"
          ? error.retryable
          : response.status >= 500,
      details: isObject(error.details) ? error.details : undefined,
    });
  }
  return body as T;
}

function projectFromUnknown(value: unknown): ProjectSummary {
  const raw =
    isObject(value) && isObject(value.project) ? value.project : value;
  if (!isObject(raw) || typeof raw.id !== "string") {
    throw new ApiClientError(500, {
      code: "INVALID_RESPONSE",
      message: "服务返回了无法识别的项目数据。",
      retryable: true,
    });
  }
  return {
    id: raw.id,
    title: stringValue(raw.title, "未命名项目"),
    status: stringValue(raw.status, "draft"),
    updatedAt: stringValue(raw.updatedAt, new Date().toISOString()),
    stableVersionId: stringValue(raw.stableVersionId) || undefined,
  };
}

function isStage(value: unknown): value is Stage {
  return typeof value === "string" && STAGES.includes(value);
}

function normalizeStage(value: unknown, stage: Stage): StageView {
  const raw = isObject(value) ? value : {};
  const artifactRef = stringValue(raw.artifactRef) || undefined;
  const requestedStatus = stringValue(raw.status, "pending");
  const allowed: StageNodeStatus[] = [
    "pending",
    "running",
    "succeeded",
    "failed",
  ];
  let status = allowed.includes(requestedStatus as StageNodeStatus)
    ? (requestedStatus as StageNodeStatus)
    : "pending";
  let integrityIssue: string | undefined;

  if (status === "succeeded" && !artifactRef) {
    status = "running";
    integrityIssue = "服务已报告完成，但尚未提供制品入口。";
  }

  return {
    stage,
    status,
    artifactRef,
    artifactType: stringValue(raw.artifactType) || undefined,
    startedAt: stringValue(raw.startedAt) || undefined,
    completedAt: stringValue(raw.completedAt) || undefined,
    integrityIssue,
  };
}

function emptyStages(): StageView[] {
  return STAGES.map((stage) => normalizeStage({}, stage));
}

function normalizeRun(value: unknown): RunView | undefined {
  if (!isObject(value) || typeof value.id !== "string") return undefined;
  const failure = isObject(value.failure) ? value.failure : {};
  const rawStages = asArray(value.stages);
  const rawArtifacts = asArray(value.stageArtifacts);
  const stages = STAGES.map((stage) => {
    const node = rawStages.find(
      (candidate) => isObject(candidate) && candidate.stage === stage,
    );
    const artifact = rawArtifacts.find(
      (candidate) => isObject(candidate) && candidate.stage === stage,
    );
    const merged =
      isObject(node) && isObject(artifact)
        ? { ...node, ...artifact }
        : (node ?? artifact ?? {});
    return normalizeStage(merged, stage);
  });

  return {
    id: value.id,
    status: stringValue(value.status, "queued") as RunStatus,
    activeAttemptId:
      stringValue(value.activeAttemptId) ||
      stringValue(value.attemptId) ||
      undefined,
    prompt: stringValue(value.prompt) || undefined,
    failureCode:
      stringValue(failure.code) ||
      stringValue(value.failureCode) ||
      stringValue(value.errorCode) ||
      undefined,
    failureMessage:
      stringValue(failure.message) ||
      stringValue(value.failureMessage) ||
      undefined,
    retryable:
      typeof failure.retryable === "boolean"
        ? failure.retryable
        : typeof value.retryable === "boolean"
          ? value.retryable
          : undefined,
    stages,
  };
}

function normalizeProjectDetail(value: unknown): ProjectDetail {
  const base = projectFromUnknown(value);
  const raw =
    isObject(value) && isObject(value.project) ? value.project : value;
  const root = isObject(raw) ? raw : {};
  const messages = asArray(root.messages)
    .filter(isObject)
    .map(
      (message, index): Message => ({
        id: stringValue(message.id, `message-${index}`),
        role: message.role === "assistant" ? "assistant" : "user",
        content: stringValue(message.content),
        createdAt: stringValue(message.createdAt) || undefined,
      }),
    );
  const runs = asArray(root.runs)
    .map(normalizeRun)
    .filter((run): run is RunView => Boolean(run));
  const activeRun = normalizeRun(root.activeRun);
  const latestRun = normalizeRun(root.latestRun) ?? runs[0];
  return { ...base, messages, activeRun, latestRun };
}

const api = {
  async listProjects(): Promise<ProjectSummary[]> {
    const body = await requestJson<unknown>(PATHS.listProjects.path);
    const items = Array.isArray(body)
      ? body
      : isObject(body) && Array.isArray(body.projects)
        ? body.projects
        : [];
    return items.map(projectFromUnknown);
  },

  async createProject(prompt: string): Promise<ProjectSummary> {
    const key = createIdempotencyKey("project");
    const body = await requestJson<unknown>(PATHS.createProject.path, {
      method: PATHS.createProject.method,
      headers: {
        "Content-Type": "application/json",
        [IDEMPOTENCY_HEADER]: key,
      },
      body: JSON.stringify({ initialPrompt: prompt, idempotencyKey: key }),
    });
    return projectFromUnknown(body);
  },

  async getProject(projectId: string): Promise<ProjectDetail> {
    const path = fillPath(PATHS.getProject.path, { id: projectId });
    return normalizeProjectDetail(await requestJson<unknown>(path));
  },

  async createRun(projectId: string, prompt: string): Promise<RunCreated> {
    const key = createIdempotencyKey("run");
    const path = fillPath(PATHS.createRun.path, { id: projectId });
    const body = await requestJson<unknown>(path, {
      method: PATHS.createRun.method,
      headers: {
        "Content-Type": "application/json",
        [IDEMPOTENCY_HEADER]: key,
      },
      body: JSON.stringify({ prompt, idempotencyKey: key }),
    });
    if (!isObject(body) || typeof body.runId !== "string") {
      throw new ApiClientError(500, {
        code: "INVALID_RESPONSE",
        message: "任务已提交，但服务没有返回任务编号。请刷新项目状态。",
        retryable: true,
      });
    }
    return {
      runId: body.runId,
      attemptId: stringValue(body.attemptId) || undefined,
    };
  },

  async retryRun(runId: string, attemptId: string): Promise<string> {
    const key = createIdempotencyKey("retry");
    const path = fillPath(PATHS.retryRun.path, { id: runId });
    const body = await requestJson<unknown>(path, {
      method: PATHS.retryRun.method,
      headers: {
        "Content-Type": "application/json",
        [IDEMPOTENCY_HEADER]: key,
      },
      body: JSON.stringify({ attemptId, idempotencyKey: key }),
    });
    return isObject(body) ? stringValue(body.attemptId, attemptId) : attemptId;
  },
};

function parseRoute(): Route {
  const match = window.location.pathname.match(/^\/project\/([^/]+)\/?$/);
  return match
    ? { kind: "project", projectId: decodeURIComponent(match[1]) }
    : { kind: "home" };
}

function useRoute() {
  const [route, setRoute] = useState<Route>(() => parseRoute());

  useEffect(() => {
    const onPopState = () => setRoute(parseRoute());
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  const navigate = useCallback((next: Route) => {
    const path =
      next.kind === "home"
        ? "/"
        : `/project/${encodeURIComponent(next.projectId)}`;
    window.history.pushState({}, "", path);
    setRoute(next);
    window.scrollTo({ top: 0, behavior: "instant" });
  }, []);

  return { route, navigate };
}

function AppIcon({
  name,
  className = "h-5 w-5",
}: {
  name: string;
  className?: string;
}) {
  const paths: Record<string, ReactNode> = {
    arrow: <path d="m5 12 14 0m-6-6 6 6-6 6" />,
    back: <path d="m19 12-14 0m6 6-6-6 6-6" />,
    bolt: <path d="M13 2 4.8 13h6.7l-.5 9L19.2 11h-6.7L13 2Z" />,
    check: <path d="m5 12 4 4L19 6" />,
    chevron: <path d="m9 18 6-6-6-6" />,
    file: <path d="M6 2h8l4 4v16H6V2Zm8 0v5h5" />,
    layers: <path d="m12 2 9 5-9 5-9-5 9-5Zm9 10-9 5-9-5m18 5-9 5-9-5" />,
    refresh: (
      <path d="M20 6v5h-5M4 18v-5h5m10.5-2A8 8 0 0 0 5.4 7M4.5 14a8 8 0 0 0 14.1 3" />
    ),
    send: <path d="m22 2-7 20-4-9-9-4 20-7Zm-11 11L22 2" />,
    spark: (
      <path d="m12 2 1.7 5.3L19 9l-5.3 1.7L12 16l-1.7-5.3L5 9l5.3-1.7L12 2Zm7 14 .8 2.2L22 19l-2.2.8L19 22l-.8-2.2L16 19l2.2-.8L19 16Z" />
    ),
    warning: <path d="M12 3 2 21h20L12 3Zm0 6v5m0 3v.1" />,
  };
  return (
    <svg
      aria-hidden="true"
      className={className}
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      {paths[name]}
    </svg>
  );
}

function Brand({ compact = false }: { compact?: boolean }) {
  return (
    <span className="inline-flex items-center gap-3">
      <span className="grid h-9 w-9 place-items-center rounded-xl bg-[#17243b] text-white shadow-[inset_0_0_0_1px_rgba(255,255,255,.14)]">
        <AppIcon name="bolt" className="h-4 w-4" />
      </span>
      {!compact && (
        <span className="text-[15px] font-black tracking-[-0.02em] text-[#17243b]">
          VIBE FORGE
        </span>
      )}
    </span>
  );
}

function TopBar({
  onHome,
  context,
}: {
  onHome: () => void;
  context?: ReactNode;
}) {
  const [health, setHealth] = useState<
    "checking" | "ready" | "waiting" | "offline"
  >("checking");

  useEffect(() => {
    let active = true;
    fetch(PATHS.health.path)
      .then((response) => {
        if (active) setHealth(response.ok ? "ready" : "waiting");
      })
      .catch(() => {
        if (active) setHealth("offline");
      });
    return () => {
      active = false;
    };
  }, []);

  const labels = {
    checking: "检查服务",
    ready: "服务在线",
    waiting: "服务待配置",
    offline: "离线模式",
  };

  return (
    <header className="sticky top-0 z-40 border-b border-[#dce2ea]/90 bg-[#f7f8fb]/90 backdrop-blur-xl">
      <div className="mx-auto flex h-16 max-w-[1440px] items-center justify-between gap-4 px-4 sm:px-6 lg:px-8">
        <button
          type="button"
          onClick={onHome}
          className="rounded-xl focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-[#1756d8]"
          aria-label="返回 Vibe Forge 首页"
        >
          <Brand />
        </button>
        <div className="min-w-0 flex-1">{context}</div>
        <div className="flex shrink-0 items-center gap-2 text-xs font-semibold text-[#5b6779]">
          <span
            className={`h-2 w-2 rounded-full ${
              health === "ready"
                ? "bg-[#1f9d78]"
                : health === "offline"
                  ? "bg-[#d65151]"
                  : "bg-[#d79528]"
            }`}
          />
          <span className="hidden sm:inline">{labels[health]}</span>
        </div>
      </div>
    </header>
  );
}

function validatePrompt(value: string): string | null {
  const length = value.trim().length;
  if (length < PROMPT_MIN_CHARS) return "写下一句你想做什么，再开始构建。";
  if (length > PROMPT_MAX_CHARS)
    return `需求最多 ${PROMPT_MAX_CHARS} 字，请先精简。`;
  return null;
}

function PromptComposer({
  value,
  onChange,
  onSubmit,
  submitting,
  disabled = false,
  variant = "home",
  error,
  disabledReason,
}: {
  value: string;
  onChange: (value: string) => void;
  onSubmit: () => void;
  submitting: boolean;
  disabled?: boolean;
  variant?: "home" | "workspace";
  error?: string | null;
  disabledReason?: string;
}) {
  const currentError =
    error ?? (value.length > PROMPT_MAX_CHARS ? validatePrompt(value) : null);
  const handleKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
      event.preventDefault();
      onSubmit();
    }
  };

  return (
    <form
      data-testid={`${variant}-composer`}
      onSubmit={(event: FormEvent) => {
        event.preventDefault();
        onSubmit();
      }}
      className={
        variant === "home"
          ? "paper-shadow rounded-[28px] border border-white bg-white/95 p-3 sm:p-4"
          : "rounded-2xl border border-[#d7dee8] bg-white p-3 shadow-[0_8px_24px_rgba(30,43,66,.07)]"
      }
    >
      <label htmlFor={`${variant}-prompt`} className="sr-only">
        {variant === "home" ? "描述想构建的产品" : "继续描述要修改的内容"}
      </label>
      <textarea
        id={`${variant}-prompt`}
        value={value}
        disabled={disabled || submitting}
        onChange={(event) => onChange(event.target.value)}
        onKeyDown={handleKeyDown}
        rows={variant === "home" ? 4 : 2}
        placeholder={
          variant === "home"
            ? "例如：做一个可以新增、完成和删除习惯的每日追踪器…"
            : "继续修改，例如：把主色改成深海蓝，并保留现有功能…"
        }
        aria-describedby={`${variant}-composer-meta${currentError ? ` ${variant}-composer-error` : ""}`}
        aria-invalid={Boolean(currentError)}
        className={`w-full resize-none bg-transparent px-3 py-3 text-[16px] leading-7 text-[#17243b] placeholder:text-[#8c98aa] focus:outline-none sm:text-lg ${
          variant === "home" ? "min-h-[132px]" : "min-h-[78px]"
        }`}
      />
      <div className="flex flex-col gap-3 border-t border-[#e9edf2] px-2 pt-3 sm:flex-row sm:items-center sm:justify-between">
        <div
          id={`${variant}-composer-meta`}
          className="flex items-center gap-3 text-xs text-[#69768a]"
        >
          <span
            className={
              value.length > PROMPT_MAX_CHARS ? "font-bold text-[#c64747]" : ""
            }
          >
            {value.length.toLocaleString()} /{" "}
            {PROMPT_MAX_CHARS.toLocaleString()}
          </span>
          <span className="hidden sm:inline">⌘ / Ctrl + Enter 发送</span>
        </div>
        <button
          type="submit"
          disabled={disabled || submitting}
          className={BUTTON_PRIMARY}
        >
          {submitting ? (
            <>
              <span className="h-4 w-4 animate-spin rounded-full border-2 border-white/40 border-t-white motion-reduce:animate-none" />
              {variant === "home" ? "正在开炉…" : "正在提交…"}
            </>
          ) : (
            <>
              {variant === "home" ? "开始构建" : "继续修改"}
              <AppIcon
                name={variant === "home" ? "arrow" : "send"}
                className="h-4 w-4"
              />
            </>
          )}
        </button>
      </div>
      {currentError && (
        <p
          id={`${variant}-composer-error`}
          role="alert"
          className="mx-2 mt-3 rounded-xl bg-[#fff1ef] px-3 py-2 text-sm font-medium text-[#a63535]"
        >
          {currentError}
        </p>
      )}
      {!currentError && disabledReason && (
        <p
          aria-live="polite"
          className="mx-2 mt-3 rounded-xl bg-[#edf3ff] px-3 py-2 text-sm font-semibold text-[#31538f]"
        >
          {disabledReason}
        </p>
      )}
    </form>
  );
}

function HomePage({
  onOpenProject,
  onCreated,
}: {
  onOpenProject: (projectId: string) => void;
  onCreated: (bootstrap: Bootstrap) => void;
}) {
  const [prompt, setPrompt] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const submittingRef = useRef(false);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [projectsState, setProjectsState] = useState<
    "loading" | "ready" | "error"
  >("loading");

  const loadProjects = useCallback(async () => {
    setProjectsState("loading");
    try {
      setProjects(await api.listProjects());
      setProjectsState("ready");
    } catch {
      setProjectsState("error");
    }
  }, []);

  useEffect(() => {
    void loadProjects();
  }, [loadProjects]);

  const submit = async () => {
    const validationError = validatePrompt(prompt);
    if (validationError) {
      setSubmitError(validationError);
      return;
    }
    if (submittingRef.current) return;
    submittingRef.current = true;
    setSubmitting(true);
    setSubmitError(null);
    const cleanPrompt = prompt.trim();
    try {
      const project = await api.createProject(cleanPrompt);
      try {
        const run = await api.createRun(project.id, cleanPrompt);
        onCreated({ project, prompt: cleanPrompt, run });
      } catch (error) {
        onCreated({
          project,
          prompt: cleanPrompt,
          runError:
            error instanceof ApiClientError
              ? error
              : new ApiClientError(500, {
                  message: "项目已保存，但构建任务未能启动。",
                  retryable: true,
                }),
        });
      }
    } catch (error) {
      setSubmitError(
        error instanceof Error ? error.message : "项目创建失败，请重试。",
      );
      submittingRef.current = false;
      setSubmitting(false);
    }
  };

  return (
    <div className="min-h-screen bg-[#f7f8fb] text-[#17243b]">
      <TopBar onHome={() => window.scrollTo({ top: 0, behavior: "smooth" })} />
      <main>
        <section className="forge-grid relative overflow-hidden border-b border-[#e2e7ed]">
          <div className="pointer-events-none absolute inset-x-0 top-0 mx-auto h-[520px] max-w-5xl bg-[radial-gradient(circle_at_center,rgba(52,107,225,.13),transparent_64%)]" />
          <div className="relative mx-auto max-w-5xl px-4 pb-16 pt-16 text-center sm:px-6 sm:pb-24 sm:pt-24">
            <div className="mb-6 inline-flex items-center gap-2 rounded-full border border-[#c8d8fa] bg-[#edf3ff] px-3 py-1.5 text-xs font-extrabold tracking-[0.14em] text-[#174db9]">
              <AppIcon name="spark" className="h-4 w-4" />
              单一执行循环 · 四个真实阶段
            </div>
            <h1 className="text-balance mx-auto max-w-4xl font-black tracking-[-0.055em] text-[#13213a] [font-size:clamp(2.7rem,7vw,5.7rem)] [line-height:.94]">
              把一个想法，
              <span className="relative whitespace-nowrap text-[#1756d8]">
                锻成能运行的产品
                <svg
                  className="absolute -bottom-3 left-1/2 h-3 w-[92%] -translate-x-1/2 text-[#e9a23b]"
                  viewBox="0 0 300 14"
                  fill="none"
                  preserveAspectRatio="none"
                  aria-hidden="true"
                >
                  <path
                    d="M3 10C72 2 215 3 297 7"
                    stroke="currentColor"
                    strokeWidth="5"
                    strokeLinecap="round"
                  />
                </svg>
              </span>
              。
            </h1>
            <p className="text-balance mx-auto mt-8 max-w-2xl text-base leading-7 text-[#5d6a7f] sm:text-lg">
              描述你想做什么。Vibe Forge 会依次完成需求、架构、代码与质量验证，
              每一步都有真实制品可查看。
            </p>

            <div className="mx-auto mt-10 max-w-3xl text-left">
              <PromptComposer
                value={prompt}
                onChange={(value) => {
                  setPrompt(value);
                  setSubmitError(null);
                }}
                onSubmit={() => void submit()}
                submitting={submitting}
                error={submitError}
              />
              <div className="mt-4 flex flex-wrap items-center justify-center gap-2">
                <span className="mr-1 text-xs font-bold uppercase tracking-[0.13em] text-[#7a8798]">
                  试试
                </span>
                {EXAMPLE_PROMPTS.map((example) => (
                  <button
                    key={example}
                    type="button"
                    onClick={() => {
                      setPrompt(example);
                      setSubmitError(null);
                    }}
                    className="rounded-full border border-[#d7dee8] bg-white/80 px-3 py-2 text-left text-xs font-semibold text-[#4b5a70] transition hover:border-[#9fb5e5] hover:text-[#174db9] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[#1756d8]"
                  >
                    {example}
                  </button>
                ))}
              </div>
            </div>
          </div>
        </section>

        <section className="mx-auto max-w-6xl px-4 py-14 sm:px-6 sm:py-20">
          <div className="mb-7 flex items-end justify-between gap-4">
            <div>
              <p className="text-xs font-black uppercase tracking-[0.16em] text-[#788496]">
                继续创作
              </p>
              <h2 className="mt-2 text-2xl font-black tracking-[-0.035em] text-[#17243b] sm:text-3xl">
                最近项目
              </h2>
            </div>
            {projectsState === "error" && (
              <button
                type="button"
                onClick={() => void loadProjects()}
                className={BUTTON_SECONDARY}
              >
                <AppIcon name="refresh" className="h-4 w-4" />
                重新加载
              </button>
            )}
          </div>

          <div aria-live="polite">
            {projectsState === "loading" && (
              <div
                className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3"
                aria-label="正在加载最近项目"
              >
                {[0, 1, 2].map((item) => (
                  <div
                    key={item}
                    className="h-44 animate-pulse rounded-2xl bg-[#e9edf3] motion-reduce:animate-none"
                  />
                ))}
              </div>
            )}

            {projectsState === "error" && (
              <div
                role="alert"
                className="rounded-2xl border border-[#f0c7c2] bg-[#fff5f3] p-6"
              >
                <p className="font-bold text-[#922f2f]">
                  最近项目暂时无法加载。
                </p>
                <p className="mt-1 text-sm text-[#7d5656]">
                  你仍然可以从上方创建新项目，或稍后重试。
                </p>
              </div>
            )}

            {projectsState === "ready" && projects.length === 0 && (
              <div className="rounded-[24px] border border-dashed border-[#bfc9d6] bg-white/65 px-6 py-12 text-center">
                <span className="mx-auto grid h-12 w-12 place-items-center rounded-2xl bg-[#eaf1ff] text-[#1756d8]">
                  <AppIcon name="layers" />
                </span>
                <h3 className="mt-4 text-lg font-black">
                  第一块作品，正等你开炉
                </h3>
                <p className="mx-auto mt-2 max-w-md text-sm leading-6 text-[#667489]">
                  在上方写下一句需求。项目保存后会出现在这里，刷新页面也能继续。
                </p>
              </div>
            )}

            {projectsState === "ready" && projects.length > 0 && (
              <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                {projects.slice(0, 6).map((project) => (
                  <button
                    key={project.id}
                    type="button"
                    onClick={() => onOpenProject(project.id)}
                    className="group min-h-44 rounded-[22px] border border-[#dce2e9] bg-white p-5 text-left shadow-[0_8px_30px_rgba(31,43,63,.05)] transition hover:-translate-y-1 hover:border-[#aebee0] hover:shadow-[0_16px_40px_rgba(31,43,63,.1)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-3 focus-visible:outline-[#1756d8] motion-reduce:transform-none"
                  >
                    <div className="flex items-start justify-between gap-4">
                      <span className="grid h-10 w-10 place-items-center rounded-xl bg-[#edf3ff] text-[#1756d8]">
                        <AppIcon name="layers" className="h-5 w-5" />
                      </span>
                      <AppIcon
                        name="arrow"
                        className="h-5 w-5 text-[#9ba7b7] transition group-hover:translate-x-1 group-hover:text-[#1756d8] motion-reduce:transform-none"
                      />
                    </div>
                    <h3 className="mt-5 truncate text-base font-black text-[#17243b]">
                      {project.title}
                    </h3>
                    <div className="mt-3 flex items-center justify-between gap-3 text-xs text-[#748196]">
                      <span className="capitalize">
                        {project.status === "active" ? "构建中" : "已保存"}
                      </span>
                      <time dateTime={project.updatedAt}>
                        {formatRelativeTime(project.updatedAt)}
                      </time>
                    </div>
                  </button>
                ))}
              </div>
            )}
          </div>
        </section>
      </main>
    </div>
  );
}

function formatRelativeTime(timestamp: string): string {
  const date = new Date(timestamp);
  if (Number.isNaN(date.getTime())) return "刚刚";
  const minutes = Math.max(
    0,
    Math.round((Date.now() - date.getTime()) / 60_000),
  );
  if (minutes < 1) return "刚刚";
  if (minutes < 60) return `${minutes} 分钟前`;
  if (minutes < 1_440) return `${Math.floor(minutes / 60)} 小时前`;
  return date.toLocaleDateString("zh-CN", { month: "short", day: "numeric" });
}

function useRunEvents(
  runId: string | undefined,
  enabled: boolean,
  onRunEvent: (eventName: string, payload: JsonObject) => void,
) {
  const [connection, setConnection] = useState<ConnectionState>(
    runId ? "connecting" : "idle",
  );
  const seenSequence = useRef(0);
  const lastEffectiveEvent = useRef(Date.now());

  useEffect(() => {
    if (!runId || !enabled) {
      setConnection("idle");
      return;
    }

    setConnection("connecting");
    const path = fillPath(PATHS.runEvents.path, { id: runId });
    const source = new EventSource(path);
    let opened = false;
    const listeners: Array<[string, EventListener]> = [];

    source.onopen = () => {
      opened = true;
      lastEffectiveEvent.current = Date.now();
      setConnection("connected");
    };
    source.onerror = () => {
      setConnection(opened ? "reconnecting" : "connecting");
    };

    EVENT_NAMES.forEach((eventName) => {
      const listener: EventListener = (rawEvent) => {
        const event = rawEvent as MessageEvent<string>;
        let payload: unknown;
        try {
          payload = JSON.parse(event.data);
        } catch {
          return;
        }
        if (!isObject(payload)) return;
        const sequence = Number(payload.seq ?? event.lastEventId);
        if (Number.isFinite(sequence) && sequence > 0) {
          if (sequence <= seenSequence.current) return;
          seenSequence.current = sequence;
        }
        lastEffectiveEvent.current = Date.now();
        setConnection("connected");
        onRunEvent(eventName, payload);
      };
      source.addEventListener(eventName, listener);
      listeners.push([eventName, listener]);
    });

    const watchdog = window.setInterval(() => {
      if (Date.now() - lastEffectiveEvent.current >= 60_000)
        setConnection("stale");
    }, 5_000);

    return () => {
      window.clearInterval(watchdog);
      listeners.forEach(([eventName, listener]) =>
        source.removeEventListener(eventName, listener),
      );
      source.close();
    };
  }, [enabled, onRunEvent, runId]);

  return connection;
}

function ConnectionBadge({ state }: { state: ConnectionState }) {
  const config: Record<ConnectionState, { label: string; color: string }> = {
    idle: { label: "状态已保存", color: "bg-[#7f8a9b]" },
    connecting: { label: "正在连接", color: "bg-[#d79528]" },
    connected: { label: "实时同步", color: "bg-[#1f9d78]" },
    reconnecting: { label: "正在重连", color: "bg-[#d79528]" },
    stale: { label: "连接停滞", color: "bg-[#c64747]" },
  };
  return (
    <span className="inline-flex items-center gap-2 rounded-full border border-[#dce2ea] bg-white px-3 py-1.5 text-xs font-bold text-[#536074]">
      <span className={`h-2 w-2 rounded-full ${config[state].color}`} />
      {config[state].label}
    </span>
  );
}

function BuildPulse({ stages }: { stages: StageView[] }) {
  return (
    <ol aria-label="Build Pulse 构建阶段" className="relative mt-6 space-y-0">
      {stages.map((node, index) => {
        const label = STAGE_LABELS[node.stage];
        const isLast = index === stages.length - 1;
        return (
          <li
            key={node.stage}
            className="relative grid grid-cols-[42px_minmax(0,1fr)] gap-3 pb-7 last:pb-0"
          >
            {!isLast && (
              <span
                aria-hidden="true"
                className={`absolute left-[20px] top-9 h-[calc(100%-18px)] w-px ${
                  node.status === "succeeded" ? "bg-[#1756d8]" : "bg-[#d7dee8]"
                }`}
              />
            )}
            <span
              className={`relative z-10 grid h-[42px] w-[42px] place-items-center rounded-full border-2 text-sm font-black ${stageNodeClass(node.status)}`}
            >
              {node.status === "succeeded" ? (
                <AppIcon name="check" className="h-5 w-5" />
              ) : node.status === "failed" ? (
                <span aria-hidden="true">!</span>
              ) : (
                index + 1
              )}
            </span>
            <div className="-mt-0.5 min-w-0 rounded-2xl border border-[#e1e6ed] bg-[#fbfcfe] px-4 py-3.5">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div>
                  <p className="text-[11px] font-black uppercase tracking-[0.13em] text-[#778397]">
                    {label.role}
                  </p>
                  <h3 className="mt-0.5 text-sm font-black text-[#1b2940]">
                    {label.title}
                  </h3>
                </div>
                <StageStatus status={node.status} />
              </div>
              <div
                className="mt-2 min-h-5 text-sm text-[#667489]"
                aria-live="polite"
              >
                {node.artifactRef ? (
                  <a
                    href={node.artifactRef}
                    target="_blank"
                    rel="noreferrer"
                    className="inline-flex items-center gap-1.5 font-bold text-[#1756d8] underline decoration-[#9bb7ee] underline-offset-4 hover:text-[#113f9e] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[#1756d8]"
                  >
                    查看{label.artifact}
                    <AppIcon name="chevron" className="h-3.5 w-3.5" />
                  </a>
                ) : node.status === "running" ? (
                  "正在生成真实制品…"
                ) : node.status === "failed" ? (
                  "本阶段未完成，可在下方重试。"
                ) : (
                  `等待生成${label.artifact}`
                )}
              </div>
              {node.integrityIssue && (
                <p
                  role="alert"
                  className="mt-2 text-xs font-semibold text-[#a63c3c]"
                >
                  {node.integrityIssue}
                </p>
              )}
            </div>
          </li>
        );
      })}
    </ol>
  );
}

function stageNodeClass(status: StageNodeStatus): string {
  if (status === "succeeded") return "border-[#1756d8] bg-[#1756d8] text-white";
  if (status === "running")
    return "border-[#e8a23a] bg-[#fff8e9] text-[#a9640b] shadow-[0_0_0_5px_rgba(232,162,58,.13)]";
  if (status === "failed")
    return "border-[#c64747] bg-[#fff1ef] text-[#a63535]";
  return "border-[#ccd4df] bg-white text-[#8a96a8]";
}

function StageStatus({ status }: { status: StageNodeStatus }) {
  const labels: Record<StageNodeStatus, string> = {
    pending: "等待",
    running: "进行中",
    succeeded: "已完成",
    failed: "失败",
  };
  const classes: Record<StageNodeStatus, string> = {
    pending: "bg-[#eef1f5] text-[#69768a]",
    running: "bg-[#fff0cc] text-[#91550d]",
    succeeded: "bg-[#e5f6f0] text-[#167659]",
    failed: "bg-[#ffe9e6] text-[#a63535]",
  };
  return (
    <span
      className={`rounded-full px-2.5 py-1 text-[11px] font-black ${classes[status]}`}
    >
      {labels[status]}
    </span>
  );
}

function WorkspacePanel({
  tab,
  project,
}: {
  tab: Exclude<WorkspaceTab, "build">;
  project: ProjectDetail | null;
}) {
  if (tab === "files") {
    return (
      <section aria-labelledby="files-title" className="h-full">
        <div className="border-b border-[#e1e6ed] px-5 py-4">
          <p className="text-[11px] font-black uppercase tracking-[0.14em] text-[#7a8697]">
            Workspace
          </p>
          <h2
            id="files-title"
            className="mt-1 text-lg font-black text-[#17243b]"
          >
            项目文件
          </h2>
        </div>
        <div className="p-5">
          <div className="overflow-hidden rounded-2xl border border-[#dce2ea] bg-[#fbfcfe]">
            {[
              { path: "/src/App.tsx", writable: true },
              { path: "/src/main.tsx", writable: false },
              { path: "/src/index.css", writable: false },
              { path: "/index.html", writable: false },
            ].map((file) => (
              <div
                key={file.path}
                className="flex items-center justify-between gap-3 border-b border-[#e5e9ef] px-4 py-3 last:border-0"
              >
                <span className="flex min-w-0 items-center gap-3 text-sm font-semibold text-[#35435a]">
                  <AppIcon
                    name="file"
                    className="h-4 w-4 shrink-0 text-[#748299]"
                  />
                  <code className="truncate">{file.path}</code>
                </span>
                <span
                  className={`shrink-0 rounded-full px-2 py-1 text-[10px] font-black ${file.writable ? "bg-[#e8f0ff] text-[#1756d8]" : "bg-[#eef1f5] text-[#7a8697]"}`}
                >
                  {file.writable ? "可编辑" : "只读"}
                </span>
              </div>
            ))}
          </div>
          <p className="mt-4 text-xs leading-5 text-[#728096]">
            文件内容与版本切换将在稳定版本生成后开放。脚手架保持只读。
          </p>
        </div>
      </section>
    );
  }

  return (
    <section
      aria-labelledby="preview-title"
      className="flex h-full min-h-[440px] flex-col"
    >
      <div className="flex items-center justify-between gap-4 border-b border-[#e1e6ed] px-5 py-4">
        <div>
          <p className="text-[11px] font-black uppercase tracking-[0.14em] text-[#7a8697]">
            Stable preview
          </p>
          <h2
            id="preview-title"
            className="mt-1 text-lg font-black text-[#17243b]"
          >
            预览
          </h2>
        </div>
        <span className="rounded-full bg-[#eef1f5] px-2.5 py-1 text-[11px] font-bold text-[#69768a]">
          {project?.stableVersionId ? "稳定版本" : "等待版本"}
        </span>
      </div>
      <div className="grid flex-1 place-items-center p-6">
        <div className="max-w-sm text-center">
          <span className="mx-auto grid h-14 w-14 place-items-center rounded-2xl border border-[#ccd9f5] bg-[#edf3ff] text-[#1756d8]">
            <AppIcon name="spark" className="h-6 w-6" />
          </span>
          <h3 className="mt-5 text-lg font-black text-[#17243b]">
            {project?.stableVersionId ? "稳定预览已准备" : "作品正在成形"}
          </h3>
          <p className="mt-2 text-sm leading-6 text-[#68768b]">
            {project?.stableVersionId
              ? "预览将在 Sandpack 工作台接入后显示；当前版本标识已由服务端保存。"
              : "只有服务端发出 preview_ready 后才会切换预览。构建失败时，上一个稳定版本会继续保留。"}
          </p>
        </div>
      </div>
    </section>
  );
}

function ProjectWorkspace({
  projectId,
  bootstrap,
  onHome,
}: {
  projectId: string;
  bootstrap?: Bootstrap;
  onHome: () => void;
}) {
  const bootstrapMatches = bootstrap?.project.id === projectId;
  const [project, setProject] = useState<ProjectDetail | null>(() =>
    bootstrapMatches
      ? {
          ...bootstrap.project,
          messages: [],
          activeRun: undefined,
          latestRun: undefined,
        }
      : null,
  );
  const [loadingState, setLoadingState] = useState<
    "loading" | "ready" | "error"
  >(bootstrapMatches ? "ready" : "loading");
  const [loadError, setLoadError] = useState<string | null>(null);
  const [runId, setRunId] = useState<string | undefined>(() =>
    bootstrapMatches ? bootstrap?.run?.runId : undefined,
  );
  const [runStatus, setRunStatus] = useState<RunStatus>(() =>
    bootstrap?.run ? "running" : bootstrap?.runError ? "failed" : "queued",
  );
  const [activeAttemptId, setActiveAttemptId] = useState<string | undefined>(
    () => (bootstrapMatches ? bootstrap?.run?.attemptId : undefined),
  );
  const [stages, setStages] = useState<StageView[]>(emptyStages);
  const [prompt, setPrompt] = useState(() =>
    bootstrapMatches && bootstrap?.runError ? bootstrap.prompt : "",
  );
  const [submitting, setSubmitting] = useState(false);
  const submittingRef = useRef(false);
  const [composerError, setComposerError] = useState<string | null>(
    bootstrapMatches ? (bootstrap?.runError?.message ?? null) : null,
  );
  const [runError, setRunError] = useState<ApiClientError | null>(
    bootstrapMatches ? (bootstrap?.runError ?? null) : null,
  );
  const [tab, setTab] = useState<WorkspaceTab>("build");
  const [desktopTab, setDesktopTab] =
    useState<Exclude<WorkspaceTab, "build">>("preview");
  const [messages, setMessages] = useState<Message[]>(() =>
    bootstrapMatches && bootstrap?.prompt
      ? [{ id: "bootstrap-prompt", role: "user", content: bootstrap.prompt }]
      : [],
  );

  const applyProject = useCallback((detail: ProjectDetail) => {
    setProject(detail);
    setMessages(detail.messages);
    const currentRun = detail.activeRun ?? detail.latestRun;
    if (currentRun) {
      setRunId(currentRun.id);
      setRunStatus(currentRun.status);
      setActiveAttemptId(currentRun.activeAttemptId);
      setStages(currentRun.stages);
      setRunError(
        currentRun.status === "failed"
          ? new ApiClientError(500, {
              code: currentRun.failureCode ?? "INTERNAL",
              message:
                currentRun.failureMessage ??
                failureMessage(currentRun.failureCode ?? "INTERNAL"),
              retryable: currentRun.retryable ?? true,
            })
          : null,
      );
    } else {
      setRunStatus("succeeded");
      setStages(emptyStages());
    }
  }, []);

  const loadProject = useCallback(async () => {
    setLoadingState("loading");
    setLoadError(null);
    try {
      const detail = await api.getProject(projectId);
      applyProject(detail);
      setLoadingState("ready");
    } catch (error) {
      setLoadError(error instanceof Error ? error.message : "项目加载失败。");
      setLoadingState("error");
    }
  }, [applyProject, projectId]);

  useEffect(() => {
    // The create response plus SSE are authoritative during the initial
    // transition. A delayed snapshot here could overwrite newer SSE events.
    if (bootstrapMatches) return undefined;
    void loadProject();
    return undefined;
  }, [bootstrapMatches, loadProject]);

  const onRunEvent = useCallback(
    (eventName: string, payload: JsonObject) => {
      if (eventName === "run_started") {
        setRunStatus("running");
        setRunError(null);
        setActiveAttemptId(stringValue(payload.attemptId) || undefined);
        return;
      }

      if (eventName === "stage_started" && isStage(payload.stage)) {
        const activeStage = payload.stage;
        setRunStatus("running");
        setStages((current) =>
          current.map((node, index) => {
            const activeIndex = STAGES.indexOf(activeStage);
            if (node.stage === activeStage) {
              return {
                ...node,
                status: "running",
                startedAt: new Date().toISOString(),
              };
            }
            if (index < activeIndex && node.artifactRef) {
              return {
                ...node,
                status: "succeeded",
                integrityIssue: undefined,
              };
            }
            return node;
          }),
        );
        return;
      }

      if (eventName === "stage_artifact" && isStage(payload.stage)) {
        const artifactStage = payload.stage;
        setStages((current) =>
          current.map((node) =>
            node.stage === artifactStage
              ? {
                  ...node,
                  artifactRef:
                    stringValue(payload.artifactRef) || node.artifactRef,
                  artifactType:
                    stringValue(payload.artifactType) || node.artifactType,
                  integrityIssue: undefined,
                }
              : node,
          ),
        );
        return;
      }

      if (eventName === "message_delta") {
        const text = stringValue(payload.text);
        if (text) {
          setMessages((current) => {
            const last = current[current.length - 1];
            if (
              last?.role === "assistant" &&
              last.id === `assistant-${runId}`
            ) {
              return [
                ...current.slice(0, -1),
                { ...last, content: `${last.content}${text}` },
              ];
            }
            return [
              ...current,
              { id: `assistant-${runId}`, role: "assistant", content: text },
            ];
          });
        }
        return;
      }

      if (eventName === "preview_ready") {
        const versionId = stringValue(payload.versionId);
        if (versionId)
          setProject((current) =>
            current ? { ...current, stableVersionId: versionId } : current,
          );
        return;
      }

      if (eventName === "run_failed") {
        const failedStage = isStage(payload.stage) ? payload.stage : undefined;
        setRunStatus("failed");
        setActiveAttemptId(stringValue(payload.attemptId) || activeAttemptId);
        setRunError(
          new ApiClientError(500, {
            code: stringValue(payload.code, "INTERNAL"),
            message: failureMessage(stringValue(payload.code)),
            retryable: payload.retryable !== false,
          }),
        );
        if (failedStage) {
          setStages((current) =>
            current.map((node) =>
              node.stage === failedStage ? { ...node, status: "failed" } : node,
            ),
          );
        }
        return;
      }

      if (eventName === "run_completed") {
        setRunStatus("succeeded");
        setRunError(null);
        setStages((current) =>
          current.map((node) =>
            node.artifactRef
              ? {
                  ...node,
                  status: "succeeded",
                  completedAt: new Date().toISOString(),
                  integrityIssue: undefined,
                }
              : {
                  ...node,
                  status: node.status === "failed" ? "failed" : "running",
                  integrityIssue: "服务已报告完成，但尚未提供制品入口。",
                },
          ),
        );
        const versionId = stringValue(payload.versionId);
        if (versionId)
          setProject((current) =>
            current ? { ...current, stableVersionId: versionId } : current,
          );
      }
    },
    [activeAttemptId, runId],
  );

  const connection = useRunEvents(
    runId,
    runStatus === "running" || runStatus === "queued",
    onRunEvent,
  );

  const submitIteration = async () => {
    const validationError = validatePrompt(prompt);
    if (validationError) {
      setComposerError(validationError);
      return;
    }
    if (
      submittingRef.current ||
      runStatus === "running" ||
      runStatus === "queued"
    )
      return;
    submittingRef.current = true;
    setSubmitting(true);
    setComposerError(null);
    try {
      const cleanPrompt = prompt.trim();
      const created = await api.createRun(projectId, cleanPrompt);
      setRunId(created.runId);
      setActiveAttemptId(created.attemptId);
      setRunStatus("running");
      setRunError(null);
      setStages(emptyStages());
      setMessages((current) => [
        ...current,
        { id: `message-${Date.now()}`, role: "user", content: cleanPrompt },
      ]);
      setPrompt("");
    } catch (error) {
      const apiError =
        error instanceof ApiClientError
          ? error
          : new ApiClientError(500, {
              message: "修改任务未能启动。",
              retryable: true,
            });
      setComposerError(
        apiError.status === 409
          ? "当前已有任务在构建中。状态同步后即可继续。"
          : apiError.message,
      );
    } finally {
      submittingRef.current = false;
      setSubmitting(false);
    }
  };

  const retry = async () => {
    if (!runId || !activeAttemptId || submittingRef.current) return;
    submittingRef.current = true;
    setSubmitting(true);
    try {
      const attemptId = await api.retryRun(runId, activeAttemptId);
      setActiveAttemptId(attemptId);
      setRunStatus("running");
      setRunError(null);
      setStages((current) =>
        current.map((node) =>
          node.status === "failed" ? { ...node, status: "pending" } : node,
        ),
      );
    } catch (error) {
      setComposerError(
        error instanceof Error ? error.message : "本轮重试失败。",
      );
    } finally {
      submittingRef.current = false;
      setSubmitting(false);
    }
  };

  const active = runStatus === "running" || runStatus === "queued";

  if (loadingState === "loading" && !project) {
    return (
      <div className="min-h-screen bg-[#f7f8fb]">
        <TopBar onHome={onHome} />
        <main
          className="grid min-h-[calc(100vh-4rem)] place-items-center px-4"
          aria-live="polite"
        >
          <div className="text-center">
            <span className="mx-auto block h-8 w-8 animate-spin rounded-full border-2 border-[#ccd5e2] border-t-[#1756d8] motion-reduce:animate-none" />
            <p className="mt-4 text-sm font-bold text-[#5f6c80]">
              正在恢复项目状态…
            </p>
          </div>
        </main>
      </div>
    );
  }

  if (loadingState === "error" && !project) {
    return (
      <div className="min-h-screen bg-[#f7f8fb]">
        <TopBar onHome={onHome} />
        <main className="grid min-h-[calc(100vh-4rem)] place-items-center px-4">
          <div
            role="alert"
            className="max-w-md rounded-3xl border border-[#efc7c1] bg-white p-7 text-center shadow-xl"
          >
            <AppIcon
              name="warning"
              className="mx-auto h-8 w-8 text-[#c64747]"
            />
            <h1 className="mt-4 text-xl font-black text-[#17243b]">
              项目没有加载完成
            </h1>
            <p className="mt-2 text-sm leading-6 text-[#6c788a]">{loadError}</p>
            <div className="mt-6 flex justify-center gap-3">
              <button
                type="button"
                onClick={onHome}
                className={BUTTON_SECONDARY}
              >
                返回首页
              </button>
              <button
                type="button"
                onClick={() => void loadProject()}
                className={BUTTON_PRIMARY}
              >
                <AppIcon name="refresh" className="h-4 w-4" />
                重试
              </button>
            </div>
          </div>
        </main>
      </div>
    );
  }

  return (
    <div className="min-h-screen bg-[#eef1f5] text-[#17243b]">
      <TopBar
        onHome={onHome}
        context={
          <div className="mx-auto flex max-w-xl items-center justify-center gap-2 truncate px-2 text-center text-sm font-bold text-[#3d4a5e]">
            <span className="hidden text-[#a1aab7] sm:inline">/</span>
            <span className="truncate">{project?.title ?? "未命名项目"}</span>
            <span className="hidden items-center gap-1 text-xs font-semibold text-[#6d7a8d] sm:inline-flex">
              <AppIcon name="check" className="h-3.5 w-3.5 text-[#1f9d78]" />
              已保存
            </span>
          </div>
        }
      />

      <nav
        aria-label="工作台区域"
        className="sticky top-16 z-30 grid grid-cols-3 border-b border-[#dce2ea] bg-white px-2 md:hidden"
      >
        {(["build", "preview", "files"] as const).map((item) => (
          <button
            key={item}
            type="button"
            onClick={() => setTab(item)}
            aria-current={tab === item ? "page" : undefined}
            className={`min-h-12 border-b-2 px-3 text-sm font-black focus-visible:outline focus-visible:outline-2 focus-visible:outline-inset focus-visible:outline-[#1756d8] ${
              tab === item
                ? "border-[#1756d8] text-[#1756d8]"
                : "border-transparent text-[#6b788b]"
            }`}
          >
            {item === "build" ? "构建" : item === "preview" ? "预览" : "文件"}
          </button>
        ))}
      </nav>

      <main className="mx-auto max-w-[1440px] p-0 md:p-4 lg:p-6">
        <div className="md:grid md:min-h-[calc(100vh-7rem)] md:grid-cols-[minmax(360px,0.82fr)_minmax(440px,1.18fr)] md:gap-4 lg:gap-6">
          <section
            aria-labelledby="build-title"
            className={`${tab === "build" ? "flex" : "hidden"} h-[calc(100dvh-7rem)] flex-col bg-white md:flex md:h-auto md:overflow-hidden md:rounded-[24px] md:border md:border-[#dce2ea] md:shadow-[0_14px_40px_rgba(34,46,66,.06)]`}
          >
            <div className="shrink-0 border-b border-[#e1e6ed] px-5 py-5 sm:px-6">
              <div className="flex flex-wrap items-start justify-between gap-4">
                <div>
                  <p className="text-[11px] font-black uppercase tracking-[0.15em] text-[#788496]">
                    Build Pulse
                  </p>
                  <h1
                    id="build-title"
                    className="mt-1 text-2xl font-black tracking-[-0.035em] text-[#17243b]"
                  >
                    从想法到可运行
                  </h1>
                  <p className="mt-2 max-w-md text-sm leading-6 text-[#69768a]">
                    一个执行循环，依次完成四个真实阶段。
                  </p>
                </div>
                <ConnectionBadge state={connection} />
              </div>
            </div>

            <div className="min-h-0 flex-1 overflow-y-auto px-5 py-5 sm:px-6">
              {messages.length > 0 && (
                <div className="mb-6 rounded-2xl border border-[#dce2ea] bg-[#f7f9fc] p-4">
                  <p className="text-[11px] font-black uppercase tracking-[0.12em] text-[#7a8697]">
                    当前需求
                  </p>
                  <p className="mt-2 line-clamp-3 whitespace-pre-wrap text-sm leading-6 text-[#39475d]">
                    {
                      [...messages]
                        .reverse()
                        .find((message) => message.role === "user")?.content
                    }
                  </p>
                </div>
              )}

              <BuildPulse stages={stages} />

              {connection === "stale" && active && (
                <div
                  role="alert"
                  className="mt-6 rounded-2xl border border-[#efd19b] bg-[#fff8e9] p-4"
                >
                  <p className="font-bold text-[#87500d]">
                    60 秒内没有收到新状态。
                  </p>
                  <p className="mt-1 text-sm leading-6 text-[#7c6544]">
                    节点不会被前端自动推进。可以刷新服务端状态，或等待连接恢复。
                  </p>
                  <button
                    type="button"
                    onClick={() => void loadProject()}
                    className={`${BUTTON_SECONDARY} mt-3`}
                  >
                    <AppIcon name="refresh" className="h-4 w-4" />
                    刷新真实状态
                  </button>
                </div>
              )}

              {runError && (
                <div
                  role="alert"
                  className="mt-6 rounded-2xl border border-[#f0c7c2] bg-[#fff5f3] p-4"
                >
                  <p className="font-bold text-[#9b3333]">{runError.message}</p>
                  <p className="mt-1 text-sm text-[#805858]">
                    项目和上一个稳定版本都已保留。
                  </p>
                  {runError.retryable &&
                    (runId && activeAttemptId ? (
                      <button
                        type="button"
                        onClick={() => void retry()}
                        disabled={submitting}
                        className={`${BUTTON_SECONDARY} mt-3`}
                      >
                        <AppIcon name="refresh" className="h-4 w-4" />
                        仅重试本轮
                      </button>
                    ) : (
                      <button
                        type="button"
                        onClick={() => void submitIteration()}
                        disabled={submitting}
                        className={`${BUTTON_SECONDARY} mt-3`}
                      >
                        <AppIcon name="refresh" className="h-4 w-4" />
                        再次启动构建
                      </button>
                    ))}
                </div>
              )}
            </div>

            <div className="shrink-0 border-t border-[#e1e6ed] bg-white p-4 sm:p-5">
              <PromptComposer
                value={prompt}
                onChange={(value) => {
                  setPrompt(value);
                  setComposerError(null);
                }}
                onSubmit={() => void submitIteration()}
                submitting={submitting}
                disabled={active}
                variant="workspace"
                error={composerError}
                disabledReason={
                  active
                    ? "当前任务正在构建中，完成或失败后可以继续修改。"
                    : undefined
                }
              />
            </div>
          </section>

          <section
            className={`${tab === "build" ? "hidden" : "block"} min-h-[calc(100vh-7rem)] bg-white md:block md:min-h-0 md:overflow-hidden md:rounded-[24px] md:border md:border-[#dce2ea] md:shadow-[0_14px_40px_rgba(34,46,66,.06)]`}
          >
            <div className="hidden border-b border-[#e1e6ed] bg-[#f8f9fb] px-3 pt-3 md:flex">
              {(["preview", "files"] as const).map((item) => (
                <button
                  key={item}
                  type="button"
                  onClick={() => setDesktopTab(item)}
                  aria-current={desktopTab === item ? "page" : undefined}
                  className={`min-h-10 rounded-t-xl px-5 text-sm font-black focus-visible:outline focus-visible:outline-2 focus-visible:outline-inset focus-visible:outline-[#1756d8] ${
                    desktopTab === item
                      ? "border border-b-white border-[#dce2ea] bg-white text-[#1756d8]"
                      : "text-[#6b788b]"
                  }`}
                >
                  {item === "preview" ? "预览" : "文件"}
                </button>
              ))}
            </div>
            <div className="h-[calc(100%-3.25rem)]">
              <WorkspacePanel
                tab={tab === "build" ? desktopTab : tab}
                project={project}
              />
            </div>
          </section>
        </div>
      </main>
    </div>
  );
}

function failureMessage(code: string): string {
  const messages: Record<string, string> = {
    TIMEOUT: "本轮构建超时，没有产生新的有效状态。",
    RATE_LIMITED: "构建服务当前繁忙，请稍后重试本轮。",
    UPSTREAM_ERROR: "生成服务暂时不可用，本轮尚未完成。",
    COMPILE_FAILED: "代码没有通过编译校验，本轮已停止。",
    INTERNAL: "构建遇到内部错误，本轮已安全停止。",
  };
  return messages[code] ?? "本轮构建未完成，可以原地重试。";
}

export default function App() {
  const { route, navigate } = useRoute();
  const [bootstrap, setBootstrap] = useState<Bootstrap | undefined>();

  if (route.kind === "home") {
    return (
      <HomePage
        onOpenProject={(projectId) => {
          setBootstrap(undefined);
          navigate({ kind: "project", projectId });
        }}
        onCreated={(nextBootstrap) => {
          setBootstrap(nextBootstrap);
          navigate({ kind: "project", projectId: nextBootstrap.project.id });
        }}
      />
    );
  }

  return (
    <ProjectWorkspace
      projectId={route.projectId}
      bootstrap={bootstrap}
      onHome={() => {
        setBootstrap(undefined);
        navigate({ kind: "home" });
      }}
    />
  );
}
