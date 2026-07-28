import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { chromium } from "playwright-core";

const baseURL = "http://127.0.0.1:5173";
const chromePath =
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const projectId = "cold-start-project";
const versionId = "88888888-8888-4888-8888-888888888888";
const contextCount = Number(process.env.PREVIEW_CONTEXT_COUNT ?? 20);
const requiredFirstAttemptSuccesses = Math.ceil(contextCount * 0.95);
const sampleTimeoutMs = Number(process.env.PREVIEW_GATE_TIMEOUT_MS ?? 25_000);

const files = {
  "/index.html":
    '<!doctype html><html><head><script src="https://cdn.tailwindcss.com"></script></head><body><div id="root"></div><script type="module" src="/src/main.tsx"></script></body></html>',
  "/src/main.tsx":
    'import { createRoot } from "react-dom/client"; import App from "./App"; createRoot(document.getElementById("root")!).render(<App />);',
  "/src/index.css": "body { margin: 0; font-family: system-ui; }",
  "/src/App.tsx":
    'import { useState } from "react"; export default function App() { const [count, setCount] = useState(0); return <main><h1>冷启动已就绪</h1><button onClick={() => setCount(count + 1)}>可交互 {count}</button></main>; }',
};

function filesHash(value) {
  const hash = createHash("sha256");
  Object.entries(value)
    .sort(([left], [right]) => (left < right ? -1 : left > right ? 1 : 0))
    .forEach(([path, content]) => {
      hash.update(path);
      hash.update(Buffer.from([0]));
      hash.update(content);
      hash.update(Buffer.from([0]));
    });
  return hash.digest("hex");
}

const version = {
  id: versionId,
  iterationId: "cold-start-iteration",
  status: "stable",
  filesHash: filesHash(files),
  createdAt: "2026-07-28T00:00:00Z",
};

const stages = ["pm", "architect", "engineer", "qa"].map((stage) => ({
  stageKey: stage,
  stage,
  status: "succeeded",
}));

const project = {
  id: projectId,
  title: "Sandpack 冷启动门禁",
  status: "active",
  stableVersionId: versionId,
  updatedAt: "2026-07-28T00:00:00Z",
  messages: [],
  workflowStatus: "completed",
  stateVersion: 1,
  stateUpdatedAt: "2026-07-28T00:00:00Z",
  responseUpdatedAt: "2026-07-28T00:00:00Z",
  stages,
  stageArtifacts: [],
  preview: { version: versionId },
  consistency: { ok: true, conflictCodes: [] },
  versions: [version],
};

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

function json(route, body, status = 200) {
  return route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
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
try {
  await waitForServer();
  browser = await chromium.launch({
    executablePath: chromePath,
    headless: true,
    args: ["--no-sandbox"],
  });

  const results = [];
  for (let index = 0; index < contextCount; index += 1) {
    const context = await browser.newContext({
      viewport: { width: 390, height: 844 },
    });
    const page = await context.newPage();
    await page.addInitScript(() => {
      window.__previewRuntimeDiagnostics = [];
      window.addEventListener("vibe-forge:preview-runtime", (event) => {
        window.__previewRuntimeDiagnostics.push(event.detail);
      });
    });
    await context.route(/cdn\.tailwindcss\.com/, (route) =>
      route.abort("timedout"),
    );
    await context.route("**/api/**", (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path === "/api/health") return json(route, { status: "healthy" });
      if (path === `/api/projects/${projectId}`) return json(route, project);
      if (path === `/api/projects/${projectId}/files`) {
        return json(route, {
          stableVersionId: versionId,
          writableFilePath: "/src/App.tsx",
          files: Object.entries(files).map(([path, content]) => ({
            path,
            content,
            readonly: path !== "/src/App.tsx",
          })),
        });
      }
      if (path === `/api/projects/${projectId}/versions`) {
        return json(route, [version]);
      }
      if (
        path ===
        `/api/projects/${projectId}/versions/${versionId}/files`
      ) {
        return json(route, files);
      }
      return json(
        route,
        { code: "NOT_FOUND", message: `unmocked ${path}` },
        404,
      );
    });

    const startedAt = Date.now();
    let ready = false;
    let diagnostic;
    let failure;
    try {
      await page.goto(`${baseURL}/project/${projectId}`);
      await page.waitForFunction(
        () =>
          document.querySelector("section[data-preview-state]")?.dataset
            .previewState === "ready",
        undefined,
        { timeout: sampleTimeoutMs },
      );
      const previewFrame = page
        .locator('iframe[title="Sandpack Preview"]')
        .contentFrame();
      const previewBody = previewFrame.locator("body");
      assert.match(await previewBody.innerText(), /冷启动已就绪/);
      await previewFrame.locator("button").evaluate((button) => button.click());
      await page.waitForTimeout(50);
      assert.match(await previewBody.innerText(), /可交互 1/);
      ready = true;
      diagnostic = await page.evaluate(() =>
        window.__previewRuntimeDiagnostics.find(
          (entry) => entry.event === "ready",
        ),
      );
    } catch (error) {
      failure = {
        message: error instanceof Error ? error.message.split("\n")[0] : String(error),
        previewState: await page
          .locator("section[data-preview-state]")
          .getAttribute("data-preview-state")
          .catch(() => undefined),
        previewErrorKind: await page
          .locator("section[data-preview-state]")
          .getAttribute("data-preview-error-kind")
          .catch(() => undefined),
        diagnostics: await page
          .evaluate(() => window.__previewRuntimeDiagnostics)
          .catch(() => []),
        iframeText: await page
          .locator('iframe[title="Sandpack Preview"]')
          .contentFrame()
          .locator("body")
          .innerText()
          .catch(() => undefined),
      };
    }
    const result = {
      sample: index + 1,
      ready,
      attempt: diagnostic?.attempt,
      runtimeMs: diagnostic?.elapsedMs,
      totalMs: Date.now() - startedAt,
      failure,
    };
    results.push(result);
    console.log(
      `preview sample ${result.sample}/${contextCount}: ready=${result.ready} attempt=${result.attempt ?? "none"} runtimeMs=${result.runtimeMs ?? "none"} totalMs=${result.totalMs}`,
    );
    if (failure) console.log(JSON.stringify(failure));
    await context.close();
  }

  const firstAttemptSuccesses = results.filter(
    (result) => result.ready && result.attempt === 0,
  ).length;
  console.table(results);
  assert.ok(
    firstAttemptSuccesses >= requiredFirstAttemptSuccesses,
    `first-attempt success ${firstAttemptSuccesses}/${contextCount}, want at least ${requiredFirstAttemptSuccesses}/${contextCount}`,
  );
  console.log(
    `preview cold-start gate passed: ${firstAttemptSuccesses}/${contextCount} fresh contexts ready on attempt 0`,
  );
} catch (error) {
  if (serverOutput) process.stderr.write(serverOutput);
  throw error;
} finally {
  await browser?.close();
  server.kill("SIGTERM");
}
