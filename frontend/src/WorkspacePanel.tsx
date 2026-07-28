import {
  Component,
  type ErrorInfo,
  type ReactNode,
  type RefObject,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  SandpackPreview,
  SandpackProvider,
  useSandpack,
  type SandpackPreviewRef,
} from "@codesandbox/sandpack-react";
import { WRITABLE_FILE_PATH } from "./contract";
import {
  createVerifiedPreview,
  isFileEditLocked,
  PREVIEW_READY_TIMEOUT_MS,
  PreviewDeadlineError,
  sandpackFiles,
  shouldStagePreview,
  withPreviewDeadline,
  type FileTree,
  type FileTreeEntry,
  type ManualIteration,
  type PreviewFailureKind,
  type PreviewSnapshot,
  type VersionView,
} from "./workspace";

export type WorkspaceDetailTab = "preview" | "files" | "versions";

export type WorkspaceApi = {
  listFiles: (projectId: string) => Promise<FileTree>;
  listVersions: (projectId: string) => Promise<VersionView[]>;
  versionFiles: (
    projectId: string,
    versionId: string,
  ) => Promise<Record<string, string>>;
  writeApp: (
    projectId: string,
    content: string,
    baseVersionId: string,
  ) => Promise<ManualIteration>;
  restoreVersion: (
    projectId: string,
    versionId: string,
  ) => Promise<ManualIteration>;
};

type Props = {
  tab: WorkspaceDetailTab;
  projectId: string;
  stableVersionId?: string;
  activeRun: boolean;
  revision: number;
  api: WorkspaceApi;
  onStableVersionChange: (versionId: string) => void;
  onRefreshProject: () => void;
};

const SANDBOX_POLICY =
  "allow-forms allow-modals allow-popups allow-presentation allow-same-origin allow-scripts";

const PRIMARY_BUTTON =
  "inline-flex min-h-10 items-center justify-center gap-2 rounded-xl bg-[#1756d8] px-4 py-2 text-sm font-bold text-white transition hover:bg-[#1248bb] disabled:cursor-not-allowed disabled:bg-[#9aa8bd] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[#1756d8]";
const SECONDARY_BUTTON =
  "inline-flex min-h-10 items-center justify-center gap-2 rounded-xl border border-[#cdd5df] bg-white px-4 py-2 text-sm font-semibold text-[#17243b] transition hover:border-[#93a2b6] hover:bg-[#f7f9fc] disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[#1756d8]";

function WorkspaceIcon({
  name,
  className = "h-4 w-4",
}: {
  name: "check" | "code" | "file" | "lock" | "refresh" | "warning";
  className?: string;
}) {
  const paths: Record<typeof name, ReactNode> = {
    check: <path d="m5 12 4 4L19 6" />,
    code: <path d="m8 9-3 3 3 3m8-6 3 3-3 3m-2-9-4 12" />,
    file: <path d="M6 2h8l4 4v16H6V2Zm8 0v5h5" />,
    lock: (
      <>
        <rect x="5" y="10" width="14" height="11" rx="2" />
        <path d="M8 10V7a4 4 0 0 1 8 0v3" />
      </>
    ),
    refresh: (
      <path d="M20 6v5h-5M4 18v-5h5m10.5-2A8 8 0 0 0 5.4 7M4.5 14a8 8 0 0 0 14.1 3" />
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

class PreviewBoundary extends Component<
  { children: ReactNode; onError: (message: string) => void },
  { failed: boolean }
> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(error: Error, _info: ErrorInfo) {
    this.props.onError(error.message);
  }

  render() {
    if (this.state.failed) return null;
    return this.props.children;
  }
}

function PreviewLifecycle({
  previewRef,
  onReady,
  onError,
}: {
  previewRef: RefObject<SandpackPreviewRef>;
  onReady: () => void;
  onError: (kind: PreviewFailureKind, message: string) => void;
}) {
  const { sandpack } = useSandpack();
  const settled = useRef(false);
  const onReadyRef = useRef(onReady);
  const onErrorRef = useRef(onError);

  useEffect(() => {
    onReadyRef.current = onReady;
    onErrorRef.current = onError;
  }, [onError, onReady]);

  useEffect(() => {
    let cancelled = false;
    let unsubscribe: (() => void) | undefined;
    let connectTimer: number | undefined;

    const reportReady = () => {
      if (settled.current) return;
      settled.current = true;
      onReadyRef.current();
    };
    const reportResourceTimeout = () => {
      if (settled.current) return;
      settled.current = true;
      onErrorRef.current(
        "resource_timeout",
        "预览资源 10 秒内没有就绪，请检查网络后重试。",
      );
    };
    const connect = () => {
      if (cancelled) return;
      const client = previewRef.current?.getClient();
      if (!client) {
        connectTimer = window.setTimeout(connect, 25);
        return;
      }
      unsubscribe = client.listen((message) => {
        // `done` may precede a failed external resource. A positive resize is
        // emitted by this iframe client only after it has rendered content.
        if (message.type === "resize" && message.height > 0) {
          reportReady();
        }
      });
    };

    connect();
    const deadline = window.setTimeout(
      reportResourceTimeout,
      PREVIEW_READY_TIMEOUT_MS,
    );
    return () => {
      cancelled = true;
      if (connectTimer !== undefined) window.clearTimeout(connectTimer);
      window.clearTimeout(deadline);
      unsubscribe?.();
    };
  }, [previewRef]);

  useEffect(() => {
    if (settled.current) return;
    if (sandpack.error) {
      const message =
        sandpack.error.message || "代码没有通过 Sandpack 编译。";
      settled.current = true;
      onErrorRef.current("runtime_error", message);
      return;
    }
    if (sandpack.status === "timeout") {
      settled.current = true;
      onErrorRef.current(
        "resource_timeout",
        "预览资源 10 秒内没有就绪，请检查网络后重试。",
      );
      return;
    }
  }, [sandpack.error, sandpack.status]);

  return null;
}

function SandboxedPreview({
  snapshot,
  onReady,
  onError,
}: {
  snapshot: PreviewSnapshot;
  onReady: () => void;
  onError: (kind: PreviewFailureKind, message: string) => void;
}) {
  const guardRef = useRef<HTMLDivElement>(null);
  const previewRef = useRef<SandpackPreviewRef>(null);
  const files = useMemo(() => sandpackFiles(snapshot.files), [snapshot.files]);

  useLayoutEffect(() => {
    const guard = guardRef.current;
    if (!guard) return undefined;
    const secureIframes = () => {
      guard.querySelectorAll("iframe").forEach((iframe) => {
        iframe.setAttribute("sandbox", SANDBOX_POLICY);
        iframe.setAttribute("referrerpolicy", "no-referrer");
        iframe.setAttribute("allow", "");
      });
    };
    secureIframes();
    const observer = new MutationObserver(secureIframes);
    observer.observe(guard, { childList: true, subtree: true });
    return () => observer.disconnect();
  }, []);

  return (
    <PreviewBoundary
      onError={(message) => onError("runtime_error", message)}
    >
      <div ref={guardRef} className="h-full min-h-[420px]">
        <SandpackProvider
          template="react-ts"
          files={files}
          customSetup={{ entry: "/src/main.tsx" }}
          options={{
            activeFile: WRITABLE_FILE_PATH,
            visibleFiles: [WRITABLE_FILE_PATH],
            autorun: true,
            autoReload: true,
            bundlerTimeOut: 10_000,
            recompileMode: "immediate",
            externalResources: ["https://cdn.tailwindcss.com"],
          }}
          theme="light"
        >
          <PreviewLifecycle
            previewRef={previewRef}
            onReady={onReady}
            onError={onError}
          />
          <SandpackPreview
            ref={previewRef}
            className="h-full"
            style={{ height: "100%", minHeight: 420 }}
            showNavigator={false}
            showOpenInCodeSandbox={false}
            showRefreshButton={false}
            showRestartButton={false}
            showSandpackErrorOverlay
          />
        </SandpackProvider>
      </div>
    </PreviewBoundary>
  );
}

function formatVersionTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "时间未知";
  return date.toLocaleString("zh-CN", {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function shortId(value: string): string {
  return value ? value.slice(0, 8) : "unknown";
}

export default function WorkspacePanel({
  tab,
  projectId,
  stableVersionId,
  activeRun,
  revision,
  api,
  onStableVersionChange,
  onRefreshProject,
}: Props) {
  const [fileTree, setFileTree] = useState<FileTree>({
    files: [],
    writableFilePath: WRITABLE_FILE_PATH,
  });
  const [versions, setVersions] = useState<VersionView[]>([]);
  const [dataState, setDataState] = useState<"loading" | "ready" | "error">(
    "loading",
  );
  const [dataError, setDataError] = useState("");
  const [selectedPath, setSelectedPath] = useState(WRITABLE_FILE_PATH);
  const [editorContent, setEditorContent] = useState("");
  const [savedContent, setSavedContent] = useState("");
  const [editorError, setEditorError] = useState("");
  const [saving, setSaving] = useState(false);
  const [restoringId, setRestoringId] = useState("");

  const [visiblePreview, setVisiblePreview] =
    useState<PreviewSnapshot | null>(null);
  const [pendingPreview, setPendingPreview] =
    useState<PreviewSnapshot | null>(null);
  const [previewState, setPreviewState] = useState<
    "idle" | "validating" | "booting" | "ready" | "error"
  >("idle");
  const [previewError, setPreviewError] = useState("");
  const [previewErrorKind, setPreviewErrorKind] =
    useState<PreviewFailureKind | null>(null);
  const [runtimeAttempt, setRuntimeAttempt] = useState(0);
  const requestSequence = useRef(0);
  const autoStagedVersion = useRef("");

  const dirty = editorContent !== savedContent;
  const selectedFile = fileTree.files.find(
    (file) => file.path === selectedPath,
  );
  const editLocked = isFileEditLocked(selectedFile, activeRun);

  const refreshWorkspaceData = useCallback(async () => {
    setDataState("loading");
    setDataError("");
    try {
      const [nextTree, nextVersions] = await Promise.all([
        api.listFiles(projectId),
        api.listVersions(projectId),
      ]);
      setFileTree(nextTree);
      setVersions(nextVersions);
      setDataState("ready");
    } catch (error) {
      setDataState("error");
      setDataError(
        error instanceof Error ? error.message : "工作区数据没有加载完成。",
      );
    }
  }, [api, projectId]);

  useEffect(() => {
    setFileTree({ files: [], writableFilePath: WRITABLE_FILE_PATH });
    setVersions([]);
    setVisiblePreview(null);
    setPendingPreview(null);
    setPreviewState("idle");
    setPreviewError("");
    setPreviewErrorKind(null);
    autoStagedVersion.current = "";
    setSelectedPath(WRITABLE_FILE_PATH);
    setEditorContent("");
    setSavedContent("");
    void refreshWorkspaceData();
  }, [projectId, refreshWorkspaceData]);

  useEffect(() => {
    if (revision > 0) void refreshWorkspaceData();
  }, [refreshWorkspaceData, revision]);

  useEffect(() => {
    const current = fileTree.files.find(
      (file) => file.path === selectedPath,
    );
    const fallback =
      fileTree.files.find((file) => file.path === WRITABLE_FILE_PATH) ??
      fileTree.files[0];
    const next = current ?? fallback;
    if (!next || dirty) return;
    setSelectedPath(next.path);
    setEditorContent(next.content);
    setSavedContent(next.content);
  }, [dirty, fileTree.files, selectedPath]);

  const stagePreview = useCallback(
    async (versionId: string) => {
      if (
        !shouldStagePreview(
          visiblePreview?.versionId,
          pendingPreview?.versionId,
          versionId,
        )
      )
        return;

      const sequence = ++requestSequence.current;
      setPreviewState("validating");
      setPreviewError("");
      setPreviewErrorKind(null);
      try {
        const [nextVersions, files] = await withPreviewDeadline(
          Promise.all([
            api.listVersions(projectId),
            api.versionFiles(projectId, versionId),
          ]),
          "validation_timeout",
          "稳定版本校验 10 秒内没有完成，请重试。",
        );
        if (sequence !== requestSequence.current) return;
        setVersions(nextVersions);
        const version = nextVersions.find((item) => item.id === versionId);
        if (!version) throw new Error("稳定版本记录尚未同步，请稍后重试。");
        const snapshot = await withPreviewDeadline(
          createVerifiedPreview(version, files),
          "validation_timeout",
          "稳定版本校验 10 秒内没有完成，请重试。",
        );
        if (sequence !== requestSequence.current) return;
        setPendingPreview(snapshot);
        setPreviewState("booting");
      } catch (error) {
        if (sequence !== requestSequence.current) return;
        setPendingPreview(null);
        setPreviewState("error");
        setPreviewErrorKind(
          error instanceof PreviewDeadlineError
            ? error.kind
            : "runtime_error",
        );
        setPreviewError(
          error instanceof Error
            ? error.message
            : "稳定预览没有加载完成。",
        );
      }
    },
    [
      api,
      pendingPreview?.versionId,
      projectId,
      visiblePreview?.versionId,
    ],
  );

  useEffect(() => {
    if (!stableVersionId) return;
    // A failed candidate must stay failed until an explicit retry; callback
    // identity changes must not silently restart the same stable version.
    if (autoStagedVersion.current === stableVersionId) return;
    autoStagedVersion.current = stableVersionId;
    void stagePreview(stableVersionId);
  }, [stableVersionId, stagePreview]);

  useEffect(() => {
    const versionId = pendingPreview?.versionId;
    if (tab !== "preview" || !versionId) {
      return undefined;
    }
    const deadline = window.setTimeout(() => {
      setPendingPreview((current) =>
        current?.versionId === versionId ? null : current,
      );
      setPreviewState("error");
      setPreviewErrorKind("resource_timeout");
      setPreviewError(
        "预览资源 10 秒内没有就绪，请检查网络后重试。 上一稳定预览未受影响。",
      );
    }, PREVIEW_READY_TIMEOUT_MS);
    return () => window.clearTimeout(deadline);
  }, [pendingPreview?.versionId, tab]);

  const handleRuntimeReady = useCallback(
    (snapshot: PreviewSnapshot) => {
      if (pendingPreview) {
        if (pendingPreview.versionId !== snapshot.versionId) return;
        setVisiblePreview(snapshot);
        setPendingPreview(null);
      } else {
        if (visiblePreview?.versionId !== snapshot.versionId) return;
      }
      setPreviewState("ready");
      setPreviewError("");
      setPreviewErrorKind(null);
      void refreshWorkspaceData();
    },
    [
      pendingPreview?.versionId,
      refreshWorkspaceData,
      visiblePreview?.versionId,
    ],
  );

  const reportPreviewError = useCallback(
    (
      snapshot: PreviewSnapshot,
      kind: PreviewFailureKind,
      message: string,
    ) => {
      const safeMessage = message.split("\n")[0] || "预览没有编译完成。";
      if (pendingPreview?.versionId === snapshot.versionId) {
        setPendingPreview(null);
      }
      setPreviewState("error");
      setPreviewErrorKind(kind);
      setPreviewError(`${safeMessage} 上一稳定预览未受影响。`);
    },
    [pendingPreview?.versionId],
  );

  const retryPreview = () => {
    setPreviewError("");
    setPreviewErrorKind(null);
    setPreviewState("booting");
    if (stableVersionId && visiblePreview?.versionId !== stableVersionId) {
      void stagePreview(stableVersionId);
      return;
    }
    setRuntimeAttempt((attempt) => attempt + 1);
  };

  const chooseFile = (file: FileTreeEntry) => {
    if (dirty && file.path !== selectedPath) {
      setEditorError("先保存或放弃当前修改，再打开其他文件。");
      return;
    }
    setSelectedPath(file.path);
    setEditorContent(file.content);
    setSavedContent(file.content);
    setEditorError("");
  };

  const saveFile = async () => {
    if (
      !stableVersionId ||
      !dirty ||
      editLocked ||
      saving ||
      selectedPath !== WRITABLE_FILE_PATH
    )
      return;
    setSaving(true);
    setEditorError("");
    try {
      const iteration = await api.writeApp(
        projectId,
        editorContent,
        stableVersionId,
      );
      if (!iteration.resultVersionId) {
        throw new Error("保存成功，但服务没有返回新版本编号。");
      }
      setSavedContent(editorContent);
      onStableVersionChange(iteration.resultVersionId);
      await refreshWorkspaceData();
    } catch (error) {
      const status = (error as { status?: number }).status;
      setEditorError(
        status === 409
          ? "稳定版本已变化或生成任务正在运行。草稿仍保留；刷新状态后可继续。"
          : error instanceof Error
            ? error.message
            : "文件没有保存，编辑草稿仍保留。",
      );
    } finally {
      setSaving(false);
    }
  };

  const restore = async (version: VersionView) => {
    if (
      activeRun ||
      restoringId ||
      version.status !== "stable" ||
      version.id === stableVersionId
    )
      return;
    setRestoringId(version.id);
    setDataError("");
    try {
      await api.restoreVersion(projectId, version.id);
      onStableVersionChange(version.id);
      await refreshWorkspaceData();
    } catch (error) {
      setDataError(
        error instanceof Error ? error.message : "版本没有恢复成功。",
      );
    } finally {
      setRestoringId("");
    }
  };

  if (tab === "preview") {
    const runtimes = [visiblePreview, pendingPreview].filter(
      (snapshot, index, items): snapshot is PreviewSnapshot =>
        Boolean(snapshot) &&
        items.findIndex(
          (candidate) => candidate?.versionId === snapshot?.versionId,
        ) === index,
    );
    const shownVersionId =
      visiblePreview?.versionId ?? pendingPreview?.versionId;

    return (
      <section
        aria-labelledby="preview-title"
        data-preview-state={previewState}
        data-preview-error-kind={previewErrorKind ?? undefined}
        className="flex h-full min-h-[540px] flex-col"
      >
        <div className="flex flex-wrap items-center justify-between gap-4 border-b border-[#e1e6ed] px-5 py-4">
          <div>
            <p className="text-[11px] font-black uppercase tracking-[0.14em] text-[#7a8697]">
              Stable preview
            </p>
            <h2
              id="preview-title"
              className="mt-1 text-lg font-black text-[#17243b]"
            >
              可交互预览
            </h2>
          </div>
          <div className="flex items-center gap-2">
            {shownVersionId && (
              <code className="rounded-full bg-[#eef1f5] px-2.5 py-1 text-[11px] font-bold text-[#69768a]">
                v/{shortId(shownVersionId)}
              </code>
            )}
            {(previewState === "error" || previewState === "ready") && (
              <button
                type="button"
                onClick={retryPreview}
                className={SECONDARY_BUTTON}
              >
                <WorkspaceIcon name="refresh" />
                刷新预览
              </button>
            )}
          </div>
        </div>

        {activeRun && (
          <div
            className="flex items-start gap-3 border-b border-[#efd7a9] bg-[#fff8e9] px-5 py-3 text-sm text-[#76531d]"
            aria-live="polite"
          >
            <span className="mt-1 h-2.5 w-2.5 shrink-0 animate-pulse rounded-full bg-[#e59a29] motion-reduce:animate-none" />
            <span>
              <strong>正在构建新版本。</strong>{" "}
              当前稳定预览保持可用，只有新版本校验完成后才会切换。
            </span>
          </div>
        )}

        {previewError && (
          <div
            role="alert"
            className="flex flex-wrap items-center justify-between gap-3 border-b border-[#f0c7c2] bg-[#fff5f3] px-5 py-3"
          >
            <span className="flex items-start gap-2 text-sm font-semibold text-[#963636]">
              <WorkspaceIcon name="warning" className="mt-0.5 h-4 w-4" />
              {previewError}
            </span>
            <button
              type="button"
              onClick={retryPreview}
              className={SECONDARY_BUTTON}
            >
              仅重试预览
            </button>
          </div>
        )}

        <div className="relative min-h-0 flex-1 overflow-hidden bg-[#e8ebef] p-3 sm:p-5">
          {runtimes.map((snapshot) => (
            <div
              key={`${snapshot.versionId}-${runtimeAttempt}`}
              className={`absolute inset-3 overflow-hidden rounded-2xl border border-[#cfd6df] bg-white shadow-[0_18px_50px_rgba(30,43,66,.13)] sm:inset-5 ${
                snapshot.versionId === shownVersionId
                  ? "z-10 opacity-100"
                  : "pointer-events-none z-0 opacity-0"
              }`}
              aria-hidden={
                snapshot.versionId === shownVersionId ? undefined : true
              }
            >
              <SandboxedPreview
                snapshot={snapshot}
                onReady={() => handleRuntimeReady(snapshot)}
                onError={(kind, message) =>
                  reportPreviewError(snapshot, kind, message)
                }
              />
            </div>
          ))}

          {!shownVersionId && (
            <div className="absolute inset-0 grid place-items-center p-6">
              <div className="max-w-sm text-center">
                <span className="mx-auto grid h-14 w-14 place-items-center rounded-2xl border border-[#ccd9f5] bg-[#edf3ff] text-[#1756d8]">
                  <WorkspaceIcon name="code" className="h-6 w-6" />
                </span>
                <h3 className="mt-5 text-lg font-black text-[#17243b]">
                  {activeRun ? "第一版正在成形" : "还没有稳定版本"}
                </h3>
                <p className="mt-2 text-sm leading-6 text-[#68768b]">
                  {activeRun
                    ? "Sandpack 会在版本事务提交并通过文件哈希校验后加载预览。"
                    : "从左侧描述要构建的产品。失败草稿不会替代这里的稳定预览。"}
                </p>
              </div>
            </div>
          )}

          {(previewState === "validating" || previewState === "booting") &&
            !visiblePreview && (
            <div
              className="pointer-events-none absolute inset-0 z-20 grid place-items-center bg-white/72 backdrop-blur-sm"
              aria-live="polite"
            >
              <div className="text-center">
                <span className="mx-auto block h-8 w-8 animate-spin rounded-full border-2 border-[#cbd5e4] border-t-[#1756d8] motion-reduce:animate-none" />
                <p className="mt-3 text-sm font-bold text-[#59687d]">
                  {previewState === "validating"
                    ? "正在校验稳定版本…"
                    : "正在启动稳定预览…"}
                </p>
              </div>
            </div>
            )}
        </div>
      </section>
    );
  }

  if (tab === "versions") {
    return (
      <section aria-labelledby="versions-title" className="h-full">
        <div className="flex items-center justify-between gap-4 border-b border-[#e1e6ed] px-5 py-4">
          <div>
            <p className="text-[11px] font-black uppercase tracking-[0.14em] text-[#7a8697]">
              Iteration history
            </p>
            <h2
              id="versions-title"
              className="mt-1 text-lg font-black text-[#17243b]"
            >
              版本
            </h2>
          </div>
          <button
            type="button"
            onClick={() => void refreshWorkspaceData()}
            className={SECONDARY_BUTTON}
          >
            <WorkspaceIcon name="refresh" />
            刷新
          </button>
        </div>

        <div className="space-y-3 p-5">
          {dataError && (
            <div
              role="alert"
              className="rounded-xl border border-[#f0c7c2] bg-[#fff5f3] p-3 text-sm font-semibold text-[#963636]"
            >
              {dataError}
            </div>
          )}
          {versions.length === 0 && dataState !== "loading" && (
            <div className="rounded-2xl border border-dashed border-[#c7d0dc] bg-[#f8fafc] p-8 text-center">
              <p className="font-black text-[#334158]">还没有版本记录</p>
              <p className="mt-1 text-sm text-[#718096]">
                首个稳定版本生成后会出现在这里。
              </p>
            </div>
          )}
          {versions.map((version, index) => {
            const current = version.id === stableVersionId;
            return (
              <article
                key={version.id}
                className={`rounded-2xl border p-4 ${
                  current
                    ? "border-[#a9c2f4] bg-[#f2f6ff]"
                    : version.status === "failed"
                      ? "border-[#edcfcb] bg-[#fffafa]"
                      : "border-[#dce2ea] bg-white"
                }`}
              >
                <div className="flex flex-wrap items-start justify-between gap-4">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-black text-[#17243b]">
                        版本 {versions.length - index}
                      </span>
                      <code className="text-xs text-[#748196]">
                        {shortId(version.id)}
                      </code>
                      <span
                        className={`rounded-full px-2 py-0.5 text-[10px] font-black ${
                          current
                            ? "bg-[#1756d8] text-white"
                            : version.status === "failed"
                              ? "bg-[#ffe8e4] text-[#a53c36]"
                              : "bg-[#e8f3ef] text-[#197258]"
                        }`}
                      >
                        {current
                          ? "当前稳定"
                          : version.status === "failed"
                            ? "失败草稿"
                            : "可恢复"}
                      </span>
                    </div>
                    <p className="mt-2 text-xs text-[#6c798d]">
                      {formatVersionTime(version.createdAt)} · iteration/
                      {shortId(version.iterationId)}
                    </p>
                    {version.filesHash && (
                      <p className="mt-1 truncate font-mono text-[10px] text-[#8b96a6]">
                        sha256 {version.filesHash}
                      </p>
                    )}
                  </div>
                  {version.status === "stable" && !current && (
                    <button
                      type="button"
                      disabled={activeRun || Boolean(restoringId)}
                      onClick={() => void restore(version)}
                      className={SECONDARY_BUTTON}
                    >
                      {restoringId === version.id ? "正在恢复…" : "恢复此版本"}
                    </button>
                  )}
                </div>
              </article>
            );
          })}
          {activeRun && (
            <p className="rounded-xl bg-[#fff8e9] p-3 text-sm font-semibold text-[#76531d]">
              生成任务运行中，版本恢复暂时锁定。
            </p>
          )}
        </div>
      </section>
    );
  }

  const writableFiles = fileTree.files.filter((file) => !file.readonly);
  const scaffoldFiles = fileTree.files.filter((file) => file.readonly);

  return (
    <section
      id="source"
      data-artifact-target="source"
      aria-labelledby="files-title"
      className="flex h-full min-h-[620px] scroll-mt-24 flex-col target:ring-2 target:ring-inset target:ring-[#1756d8]"
    >
      <div className="flex flex-wrap items-center justify-between gap-4 border-b border-[#e1e6ed] px-5 py-4">
        <div>
          <p className="text-[11px] font-black uppercase tracking-[0.14em] text-[#7a8697]">
            Workspace
          </p>
          <h2 id="files-title" className="mt-1 text-lg font-black text-[#17243b]">
            文件与编辑器
          </h2>
        </div>
        <div className="flex items-center gap-2 text-xs font-bold">
          {dirty ? (
            <span className="rounded-full bg-[#fff0cc] px-2.5 py-1 text-[#91550d]">
              未保存
            </span>
          ) : (
            <span className="inline-flex items-center gap-1 rounded-full bg-[#e5f6f0] px-2.5 py-1 text-[#167659]">
              <WorkspaceIcon name="check" className="h-3 w-3" />
              已同步
            </span>
          )}
        </div>
      </div>

      {activeRun && (
        <div
          className="flex items-start gap-2 border-b border-[#efd7a9] bg-[#fff8e9] px-5 py-3 text-sm font-semibold text-[#76531d]"
          aria-live="polite"
        >
          <WorkspaceIcon name="lock" className="mt-0.5 h-4 w-4 shrink-0" />
          智能体正在写入新版本，人工保存已锁定。现有草稿会保留，任务结束后可继续。
        </div>
      )}

      <div className="grid min-h-0 flex-1 md:grid-cols-[210px_minmax(0,1fr)]">
        <aside className="border-b border-[#e1e6ed] bg-[#f8fafc] p-3 md:border-b-0 md:border-r">
          <p className="px-2 pb-2 text-[10px] font-black uppercase tracking-[0.13em] text-[#7c8898]">
            生成业务文件
          </p>
          {writableFiles.map((file) => (
            <FileButton
              key={file.path}
              file={file}
              selected={file.path === selectedPath}
              onClick={() => chooseFile(file)}
            />
          ))}
          <p className="mt-5 px-2 pb-2 text-[10px] font-black uppercase tracking-[0.13em] text-[#7c8898]">
            固定脚手架
          </p>
          {scaffoldFiles.map((file) => (
            <FileButton
              key={file.path}
              file={file}
              selected={file.path === selectedPath}
              onClick={() => chooseFile(file)}
            />
          ))}
          {fileTree.files.length === 0 && (
            <p className="rounded-xl border border-dashed border-[#cbd3de] p-3 text-xs leading-5 text-[#718096]">
              稳定版本生成后，将从服务端恢复固定文件树。
            </p>
          )}
        </aside>

        <div className="flex min-h-0 flex-col bg-[#111827]">
          <div className="flex min-h-12 items-center justify-between gap-3 border-b border-white/10 px-4">
            <code className="truncate text-xs font-semibold text-[#d5deeb]">
              {selectedPath}
            </code>
            <span className="inline-flex shrink-0 items-center gap-1 text-[10px] font-bold uppercase tracking-[0.1em] text-[#8fa0b7]">
              {selectedFile?.readonly ? (
                <>
                  <WorkspaceIcon name="lock" className="h-3 w-3" />
                  read only
                </>
              ) : (
                "tsx"
              )}
            </span>
          </div>
          <textarea
            aria-label={`${selectedPath} 代码编辑器`}
            value={editorContent}
            readOnly={editLocked}
            spellCheck={false}
            onChange={(event) => {
              setEditorContent(event.target.value);
              setEditorError("");
            }}
            className="min-h-[360px] flex-1 resize-none bg-[#111827] p-4 font-mono text-[13px] leading-6 text-[#d9e3f0] caret-[#75a7ff] focus:outline-none focus:ring-2 focus:ring-inset focus:ring-[#4e82e6] read-only:cursor-default read-only:text-[#a9b6c7]"
          />
          <div className="border-t border-white/10 bg-[#172033] p-3">
            {editorError && (
              <div
                role="alert"
                className="mb-3 rounded-xl bg-[#40282d] p-3 text-sm font-semibold text-[#ffc6c0]"
              >
                {editorError}
                <div className="mt-2 flex flex-wrap gap-2">
                  <button
                    type="button"
                    onClick={onRefreshProject}
                    className="rounded-lg border border-white/20 px-3 py-1.5 text-xs font-bold text-white hover:bg-white/10"
                  >
                    刷新项目状态
                  </button>
                  {dirty && (
                    <button
                      type="button"
                      onClick={() => {
                        setEditorContent(savedContent);
                        setEditorError("");
                      }}
                      className="rounded-lg border border-white/20 px-3 py-1.5 text-xs font-bold text-white hover:bg-white/10"
                    >
                      放弃本地草稿
                    </button>
                  )}
                </div>
              </div>
            )}
            <div className="flex flex-wrap items-center justify-between gap-3">
              <p className="text-xs text-[#93a3b8]">
                {selectedFile?.readonly
                  ? "脚手架由系统固定，不能人工修改。"
                  : activeRun
                    ? "生成运行中，保存与智能体写入互斥。"
                    : "保存会创建独立 manual iteration，并校验新预览。"}
              </p>
              {!selectedFile?.readonly && (
                <button
                  type="button"
                  disabled={
                    !dirty || editLocked || saving || !stableVersionId
                  }
                  onClick={() => void saveFile()}
                  className={PRIMARY_BUTTON}
                >
                  {saving ? "正在保存…" : "保存并编译"}
                </button>
              )}
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

function FileButton({
  file,
  selected,
  onClick,
}: {
  file: FileTreeEntry;
  selected: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-current={selected ? "page" : undefined}
      className={`mb-1 flex w-full items-center gap-2 rounded-xl px-2.5 py-2 text-left text-xs font-semibold focus-visible:outline focus-visible:outline-2 focus-visible:outline-[#1756d8] ${
        selected
          ? "bg-[#e8f0ff] text-[#174db9]"
          : "text-[#4e5d72] hover:bg-[#eef2f7]"
      }`}
    >
      <WorkspaceIcon
        name={file.readonly ? "lock" : "file"}
        className="h-3.5 w-3.5 shrink-0"
      />
      <code className="truncate">{file.path.replace(/^\/src\//, "")}</code>
    </button>
  );
}
