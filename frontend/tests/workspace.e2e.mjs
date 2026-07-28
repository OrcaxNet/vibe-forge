import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdir } from "node:fs/promises";
import { spawn } from "node:child_process";
import { chromium } from "playwright-core";

const baseURL = "http://127.0.0.1:5173";
const chromePath =
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const projectId = "project-1";
const versionOne = "11111111-1111-4111-8111-111111111111";
const versionTwo = "22222222-2222-4222-8222-222222222222";
const productSpec =
  "# Product Spec: 计算器\n\n中文 空格\n换行 # `code` should stay out of the URL";
const architecturePlan = "## Architecture\n\nReact → API → SQLite";
const compileResult = JSON.stringify({
  pass: true,
  filesHash: "verified-build-hash",
  errors: [],
});

const scaffold = {
  "/index.html": `<!doctype html><html><head><script src="https://cdn.tailwindcss.com"></script></head><body><div id="root"></div><script type="module" src="/src/main.tsx"></script></body></html>`,
  "/src/main.tsx": `import { createRoot } from "react-dom/client"; import App from "./App"; import "./index.css"; createRoot(document.getElementById("root")!).render(<App />);`,
  "/src/index.css": "body { margin: 0; font-family: system-ui; }",
};

const firstApp = `import { useState } from "react";
export default function App() {
  const [items, setItems] = useState(["阅读"]);
  return <main className="min-h-screen bg-slate-50 p-8">
    <h1 className="text-2xl font-bold">每日习惯追踪器</h1>
    <button onClick={() => setItems([...items, "新习惯"])}>新增习惯</button>
    {items.map((item, index) => <div key={index}>{item}<button onClick={() => setItems(items.filter((_, i) => i !== index))}>删除</button></div>)}
  </main>;
}`;

const secondApp = `import { useState } from "react";
export default function App() {
  const [items, setItems] = useState(["阅读"]);
  return <main className="min-h-screen bg-sky-50 p-8 text-sky-950">
    <h1 className="text-2xl font-bold text-sky-700">习惯追踪器 · 海蓝</h1>
    <button onClick={() => setItems([...items, "新习惯"])}>新增习惯</button>
    {items.map((item, index) => <div key={index}>{item}<button onClick={() => setItems(items.filter((_, i) => i !== index))}>删除</button></div>)}
  </main>;
}`;

const maps = {
  [versionOne]: { ...scaffold, "/src/App.tsx": firstApp },
  [versionTwo]: { ...scaffold, "/src/App.tsx": secondApp },
};

function filesHash(files) {
  const hash = createHash("sha256");
  Object.entries(files)
    .sort(([left], [right]) => (left < right ? -1 : left > right ? 1 : 0))
    .forEach(([path, content]) => {
      hash.update(path);
      hash.update(Buffer.from([0]));
      hash.update(content);
      hash.update(Buffer.from([0]));
    });
  return hash.digest("hex");
}

const versions = [
  {
    id: versionOne,
    iterationId: "iteration-1",
    status: "stable",
    filesHash: filesHash(maps[versionOne]),
    createdAt: "2026-07-28T00:00:00Z",
  },
  {
    id: versionTwo,
    iterationId: "iteration-2",
    status: "stable",
    filesHash: filesHash(maps[versionTwo]),
    createdAt: "2026-07-28T00:10:00Z",
  },
];

let stableVersionId = versionOne;
let activeRunMode = false;
let workflowMode = "completed";
let projectRequestFails = false;
let delayNextProject = true;
let releaseFirstProjectResponse;
const firstProjectResponseGate = new Promise((resolve) => {
  releaseFirstProjectResponse = resolve;
});
let validationBlocked = true;
let runtimeBlocked = false;
let runtimeDocumentCount = 0;

function project() {
  const completedStages = [
    {
      stageKey: "pm",
      stage: "pm",
      status: "succeeded",
      artifactType: "spec",
      artifactRef: productSpec,
    },
    {
      stageKey: "architect",
      stage: "architect",
      status: "succeeded",
      artifactType: "structure_plan",
      artifactRef: architecturePlan,
    },
    {
      stageKey: "engineer",
      stage: "engineer",
      status: "succeeded",
      artifactType: "file_ref",
      artifactRef: `${versionOne}:/src/App.tsx`,
    },
    {
      stageKey: "qa",
      stage: "qa",
      status: workflowMode === "recovering" ? "recovering" : "succeeded",
      artifactType: "compile_result",
      artifactRef: compileResult,
    },
  ];
  const activeStages = completedStages.map((stage, index) => ({
    ...stage,
    status: index === 0 ? "running" : "waiting",
    artifactRef: undefined,
    artifactType: undefined,
  }));
  const completedRun = {
    id: "run-complete",
    status: "succeeded",
    stages: completedStages,
    stageArtifacts: completedStages,
  };
  const activeRun = {
    id: "run-active",
    status: "running",
    prompt: "把卡片改成圆角",
    activeAttemptId: "attempt-active",
    stages: activeStages,
    stageArtifacts: [],
  };
  return {
    id: projectId,
    title: "每日习惯追踪器",
    status: "active",
    updatedAt: "2026-07-28T00:10:00Z",
    stableVersionId,
    messages: [
      {
        id: "message-1",
        role: "user",
        content: "做一个可以新增和删除习惯的追踪器",
      },
    ],
    workflowStatus: activeRunMode ? "running" : workflowMode,
    workflowRunId: activeRunMode ? "run-active" : "run-complete",
    stateVersion: activeRunMode ? 10 : workflowMode === "recovering" ? 11 : 9,
    stateUpdatedAt: "2026-07-28T00:10:00Z",
    responseUpdatedAt: new Date().toISOString(),
    stages: activeRunMode ? activeStages : completedStages,
    preview: {
      version: stableVersionId,
      workflowRunId: "run-complete",
    },
    consistency:
      workflowMode === "recovering" && !activeRunMode
        ? {
            ok: false,
            conflictCodes: ["REQUIRED_STAGE_NOT_SUCCEEDED"],
          }
        : { ok: true, conflictCodes: [] },
    latestRun: activeRunMode ? activeRun : completedRun,
    activeRun: activeRunMode ? activeRun : undefined,
    runs: activeRunMode ? [completedRun, activeRun] : [completedRun],
    versions: versions.filter(
      (version) =>
        version.id === versionOne || stableVersionId === versionTwo,
    ),
  };
}

function fileTree() {
  return {
    stableVersionId,
    writableFilePath: "/src/App.tsx",
    files: Object.entries(maps[stableVersionId]).map(([path, content]) => ({
      path,
      content,
      readonly: path !== "/src/App.tsx",
    })),
  };
}

async function waitForServer() {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(baseURL);
      if (response.ok) return;
    } catch {
      // Vite is still starting.
    }
    await new Promise((resolve) => setTimeout(resolve, 150));
  }
  throw new Error("Vite did not start within 20 seconds");
}

const server = spawn("npm", ["run", "dev", "--", "--host", "127.0.0.1"], {
  stdio: ["ignore", "pipe", "pipe"],
});
let serverOutput = "";
server.stdout.on("data", (chunk) => {
  serverOutput += chunk.toString();
});
server.stderr.on("data", (chunk) => {
  serverOutput += chunk.toString();
});

let browser;
let page;
let consoleErrors = [];
try {
  await waitForServer();
  browser = await chromium.launch({
    executablePath: chromePath,
    headless: true,
    args: ["--no-sandbox"],
  });
  page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  consoleErrors = [];
  page.on("console", (message) => {
    if (message.type() === "error") consoleErrors.push(message.text());
  });

  await page.context().route(/cdn\.tailwindcss\.com/, (route) =>
    route.abort("timedout"),
  );
  await page
    .context()
    .route(/https:\/\/.*sandpack\.codesandbox\.io\/.*/, (route) => {
      if (
        runtimeBlocked &&
        route.request().resourceType() === "document" &&
        ++runtimeDocumentCount >= 2
      ) {
        return route.fulfill({
          status: 200,
          contentType: "text/html; charset=utf-8",
          body: `<!doctype html><html><body><main><h1>iframe 已渲染，运行协议未就绪</h1></main></body></html>`,
        });
      }
      return route.continue();
    });

  await page.context().route("**/api/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    const json = (body, status = 200) =>
      route.fulfill({
        status,
        contentType: "application/json",
        body: JSON.stringify(body),
      });

    if (path === "/api/health") return json({ status: "healthy" });
    if (path === "/api/runs/run-active/events") {
      return route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: "",
      });
    }
    if (path === `/api/projects/${projectId}`) {
      if (projectRequestFails) {
        return json(
          {
            code: "UPSTREAM_ERROR",
            message: "状态服务暂时不可用。",
            retryable: true,
          },
          503,
        );
      }
      if (delayNextProject) {
        delayNextProject = false;
        await firstProjectResponseGate;
      }
      return json(project());
    }
    if (path === `/api/projects/${projectId}/files`) return json(fileTree());
    if (path === `/api/projects/${projectId}/versions`) {
      return json(
        versions.filter(
          (version) =>
            version.id === versionOne || stableVersionId === versionTwo,
        ),
      );
    }
    if (
      path ===
      `/api/projects/${projectId}/versions/${versionOne}/files`
    ) {
      if (validationBlocked) {
        await new Promise((resolve) => setTimeout(resolve, 11_000));
      }
      return json(maps[versionOne]);
    }
    if (
      path ===
      `/api/projects/${projectId}/versions/${versionTwo}/files`
    ) {
      return json(maps[versionTwo]);
    }
    if (
      path === `/api/projects/${projectId}/files/src/App.tsx` &&
      request.method() === "PUT"
    ) {
      const body = request.postDataJSON();
      assert.equal(body.baseVersionId, versionOne);
      assert.match(body.content, /海蓝/);
      stableVersionId = versionTwo;
      return json(
        {
          id: "manual-iteration-2",
          baseVersionId: versionOne,
          resultVersionId: versionTwo,
        },
        202,
      );
    }
    return json(
      { code: "NOT_FOUND", message: `unmocked ${request.method()} ${path}` },
      404,
    );
  });

  const firstNavigation = page.goto(`${baseURL}/project/${projectId}`);
  await page.getByTestId("build-pulse-skeleton").waitFor();
  assert.equal(
    await page.getByText("等待", { exact: true }).count(),
    0,
    "server state must not be represented as waiting before the snapshot arrives",
  );
  releaseFirstProjectResponse();
  await firstNavigation;
  await page.getByRole("heading", { name: "从想法到可运行" }).waitFor();
  await page.getByRole("heading", { name: "可交互预览" }).waitFor();
  assert.equal(
    await page.getByText("已完成", { exact: true }).count(),
    4,
    "a completed project must restore all durable stage states",
  );
  await mkdir("test-results", { recursive: true });
  await page.screenshot({
    path: "test-results/workflow-completed.png",
    fullPage: true,
  });

  const artifactLinks = [
    ["查看产品规格", "product-spec"],
    ["查看结构方案", "architecture"],
    ["查看源文件", "source"],
    ["查看编译结果", "build-result"],
  ];
  for (const [name, target] of artifactLinks) {
    const link = page.getByRole("link", { name });
    assert.equal(
      await link.getAttribute("href"),
      `#${target}`,
      `${name} should use a stable short target`,
    );
    assert.equal(await link.getAttribute("target"), "_blank");
  }

  const popupPromise = page.context().waitForEvent("page");
  await page.getByRole("link", { name: "查看产品规格" }).click();
  const artifactPage = await popupPromise;
  await artifactPage
    .locator('[data-artifact-target="product-spec"]')
    .waitFor();
  assert.equal(
    artifactPage.url(),
    `${baseURL}/project/${projectId}#product-spec`,
  );
  assert.ok(!artifactPage.url().includes("Product%20Spec"));
  await artifactPage
    .getByText("中文 空格", { exact: false })
    .waitFor();
  await artifactPage.reload();
  await artifactPage
    .locator('[data-artifact-target="product-spec"]')
    .waitFor();
  assert.equal(artifactPage.url().split("#")[1], "product-spec");
  await mkdir("test-results", { recursive: true });
  await artifactPage.screenshot({
    path: "test-results/artifact-product-spec.png",
    fullPage: true,
  });
  await artifactPage.close();

  const copiedLinkPagePromise = page.context().waitForEvent("page");
  await page.evaluate(
    (url) => window.open(url, "_blank", "noopener"),
    `${baseURL}/project/${projectId}#product-spec`,
  );
  const copiedLinkPage = await copiedLinkPagePromise;
  for (const [name, target] of artifactLinks) {
    await copiedLinkPage.goto(`${baseURL}/project/${projectId}#${target}`);
    await copiedLinkPage.locator(`[data-artifact-target="${target}"]`).waitFor();
    assert.equal(copiedLinkPage.url().split("#")[1], target, name);
  }
  await copiedLinkPage.close();

  await page
    .locator('section[data-preview-error-kind="validation_timeout"]')
    .waitFor({ timeout: 12_000 });
  await page
    .getByText("稳定版本校验 10 秒内没有完成，请重试。")
    .waitFor();
  assert.equal(
    await page.locator('iframe[title="Sandpack Preview"]').count(),
    0,
  );

  validationBlocked = false;
  await page.getByRole("button", { name: "仅重试预览" }).click();
  const previewFrame = page
    .locator('iframe[title="Sandpack Preview"]')
    .first()
    .contentFrame();
  await previewFrame.getByText("每日习惯追踪器").waitFor({ timeout: 20_000 });
  await page
    .locator('section[data-preview-state="ready"]')
    .waitFor({ timeout: 20_000 });
  await page.screenshot({
    path: "test-results/workflow-completed.png",
    fullPage: true,
  });
  assert.equal(
    await page.getByText("正在启动稳定预览…").isVisible().catch(() => false),
    false,
  );

  const iframe = page.locator('iframe[title="Sandpack Preview"]').first();
  const sandbox = await iframe.getAttribute("sandbox");
  assert.ok(sandbox?.includes("allow-scripts"));
  assert.ok(!sandbox?.includes("allow-top-navigation"));
  assert.ok(sandbox?.includes("allow-same-origin"));
  await page.context().addCookies([
    {
      name: "host_secret",
      value: "must-not-cross-preview-boundary",
      url: baseURL,
    },
  ]);
  const previewCookies = await previewFrame.locator("body").evaluate(
    () => document.cookie,
  );
  assert.ok(!previewCookies.includes("host_secret"));
  const canReadTop = await previewFrame.locator("body").evaluate(() => {
    try {
      void window.top.location.href;
      return true;
    } catch {
      return false;
    }
  });
  assert.equal(canReadTop, false);

  await page.getByRole("button", { name: "文件" }).click();
  await page.getByRole("button", { name: "版本" }).click();
  await page.getByText("当前稳定", { exact: true }).waitFor();
  await page.getByRole("button", { name: "预览" }).click();
  await page
    .locator('iframe[title="Sandpack Preview"]')
    .first()
    .contentFrame()
    .getByText("每日习惯追踪器")
    .waitFor({ timeout: 20_000 });
  await page
    .locator('section[data-preview-state="ready"]')
    .waitFor({ timeout: 20_000 });
  assert.equal(
    await page.getByText("正在启动稳定预览…").isVisible().catch(() => false),
    false,
  );
  assert.equal(
    await page.getByText("已完成", { exact: true }).count(),
    4,
    "switching workspace tabs must not rewrite stage state",
  );

  await page.getByRole("button", { name: "文件" }).click();
  const editor = page.getByLabel("/src/App.tsx 代码编辑器");
  await editor.waitFor();
  await editor.fill(secondApp);
  await page.getByText("未保存", { exact: true }).waitFor();
  await page.getByRole("button", { name: "保存并编译" }).click();
  await page.getByText("已同步", { exact: true }).waitFor();

  runtimeBlocked = true;
  runtimeDocumentCount = 0;
  await page.getByRole("button", { name: "预览" }).click();
  await page
    .locator('section[data-preview-error-kind="resource_timeout"]')
    .waitFor({ timeout: 20_000 });
  await page
    .getByText("预览资源 10 秒内没有就绪，请检查网络后重试。", {
      exact: false,
    })
    .waitFor();
  await page
    .locator('iframe[title="Sandpack Preview"]')
    .first()
    .contentFrame()
    .getByText("每日习惯追踪器")
    .waitFor();
  await page.getByRole("heading", { name: "从想法到可运行" }).waitFor();
  assert.equal(
    await page.locator('iframe[title="Sandpack Preview"]').count(),
    1,
  );

  runtimeBlocked = false;
  await page.getByRole("button", { name: "仅重试预览" }).click();
  const updatedPreviewFrame = page
    .locator('iframe[title="Sandpack Preview"]')
    .last()
    .contentFrame();
  await updatedPreviewFrame.getByText("习惯追踪器 · 海蓝").waitFor({
    timeout: 20_000,
  });
  await page
    .locator('section[data-preview-state="ready"]')
    .waitFor({ timeout: 20_000 });
  await page.getByText("v/22222222").waitFor();
  await page.waitForFunction(
    () => document.querySelectorAll('iframe[title="Sandpack Preview"]').length === 1,
  );
  await updatedPreviewFrame.getByRole("button", { name: "新增习惯" }).click();
  await updatedPreviewFrame.getByText("新习惯").waitFor();
  await updatedPreviewFrame
    .getByRole("button", { name: "删除" })
    .first()
    .click();

  await page.getByRole("button", { name: "版本" }).click();
  await page.getByText("当前稳定", { exact: true }).waitFor();
  await page.getByRole("button", { name: "恢复此版本" }).waitFor();

  await page.setViewportSize({ width: 375, height: 812 });
  await page.reload();
  await page.getByRole("button", { name: "构建", exact: true }).waitFor();
  await page.getByRole("button", { name: "版本", exact: true }).waitFor();
  const hasHorizontalOverflow = await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  );
  assert.equal(hasHorizontalOverflow, false);

  activeRunMode = true;
  await page.reload();
  await page.getByRole("button", { name: "文件", exact: true }).click();
  await page.getByText("人工保存已锁定", { exact: false }).waitFor();
  assert.equal(
    await page.getByLabel("/src/App.tsx 代码编辑器").isEditable(),
    false,
  );
  projectRequestFails = true;
  await page.getByRole("button", { name: "构建", exact: true }).click();
  await page
    .getByTestId("workflow-stale-cache")
    .waitFor({ timeout: 6_000 });
  await page
    .getByText("状态刷新失败，已保留上次验证结果。", { exact: true })
    .waitFor();
  projectRequestFails = false;

  await mkdir("test-results", { recursive: true });
  await page.screenshot({
    path: "test-results/workspace-mobile.png",
    fullPage: true,
  });

  activeRunMode = false;
  workflowMode = "recovering";
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.reload();
  await page.getByTestId("workflow-recovering").waitFor();
  await page.getByText("恢复中", { exact: true }).waitFor();
  await page
    .locator('section[data-preview-state="ready"]')
    .waitFor({ timeout: 20_000 });
  await page.screenshot({
    path: "test-results/workflow-recovering.png",
    fullPage: true,
  });
  assert.ok(
    consoleErrors.some((message) =>
      message.includes("workflow_state_conflict"),
    ),
    "a consistency conflict should be reported",
  );

  const relevantErrors = consoleErrors.filter(
    (message) =>
      !message.includes("cdn.tailwindcss.com") &&
      message !== "Failed to load resource: net::ERR_TIMED_OUT" &&
      !message.includes("Download the React DevTools") &&
      !message.includes("workflow_state_conflict") &&
      !message.includes("status of 503"),
  );
  assert.deepEqual(relevantErrors, []);
  console.log(
    "workspace e2e passed: no waiting flash, completed restore, four stable artifact links, copied/refreshed/new-tab targets, validation timeout, initial load, tab roundtrip state isolation, rendered iframe with CDN timeout, resource timeout, retry success, duplicate ready dedupe, sandbox, host continuity, edit lock, trusted-cache failure, recovering conflict report, versions, 375px",
  );
} catch (error) {
  releaseFirstProjectResponse?.();
  console.error(serverOutput);
  if (page) {
    console.error(await page.locator("body").innerText().catch(() => ""));
    console.error(
      "preview state:",
      await page
        .locator("section[data-preview-state]")
        .evaluate((node) => ({
          state: node.getAttribute("data-preview-state"),
          errorKind: node.getAttribute("data-preview-error-kind"),
        }))
        .catch(() => null),
      "runtime documents:",
      runtimeDocumentCount,
    );
    console.error("browser console errors:", consoleErrors);
    console.error(
      "preview iframe:",
      await page
        .locator('iframe[title="Sandpack Preview"]')
        .evaluate((node) => ({
          src: node.src,
          sandbox: node.getAttribute("sandbox"),
        }))
        .catch(() => null),
    );
    console.error(
      "frames:",
      page.frames().map((frame) => frame.url()),
    );
    console.error(
      "preview bodies:",
      await Promise.all(
        page
          .frames()
          .slice(1)
          .map((frame) => frame.locator("body").innerText().catch(() => "")),
      ),
    );
    await mkdir("test-results", { recursive: true });
    await page
      .screenshot({ path: "test-results/workspace-failure.png", fullPage: true })
      .catch(() => undefined);
  }
  throw error;
} finally {
  if (browser) await browser.close();
  server.kill("SIGTERM");
  await new Promise((resolve) => {
    const timer = setTimeout(resolve, 2_000);
    server.once("exit", () => {
      clearTimeout(timer);
      resolve();
    });
  });
}
