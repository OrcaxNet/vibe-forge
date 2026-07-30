import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdir } from "node:fs/promises";
import { chromium } from "playwright-core";

const baseURL = "http://127.0.0.1:41731";
const chromePath =
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";

const titles = [
  "FLO-78：做一个点击计数器，支持加一、减一和重置，界面简洁。",
  "this-is-a-deliberately-long-unbroken-english-project-title-that-must-not-expand-the-card",
  "123e4567-e89b-12d3-a456-426614174000",
  "做一个待办清单，支持新增、完成、删除和清空已完成。",
  "计数器 Demo",
  "生成一个食谱收藏页，支持新增食谱、关键词筛选和删除。",
  "第七个项目：验证展开后可以找回并打开历史项目。",
];
let projectCount = 6;
let projectsResponse = "success";
let projectListRequests = 0;

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

const server = spawn(
  process.execPath,
  [
    "node_modules/vite/bin/vite.js",
    "--host",
    "127.0.0.1",
    "--port",
    "41731",
    "--strictPort",
  ],
  { stdio: ["ignore", "pipe", "pipe"] },
);
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
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
  const consoleErrors = [];
  page.on("console", (message) => {
    if (message.type() === "error") consoleErrors.push(message.text());
  });

  await page.context().route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/auth/session") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          authenticated: true,
          expiresAt: "2026-07-30T20:00:00Z",
        }),
      });
    }
    if (path === "/api/health") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ status: "healthy" }),
      });
    }
    if (path === "/api/projects") {
      projectListRequests += 1;
      if (projectsResponse === "error") {
        return route.fulfill({
          status: 503,
          contentType: "application/json",
          body: JSON.stringify({
            code: "UNAVAILABLE",
            message: "project list unavailable",
          }),
        });
      }
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          projects: titles.slice(0, projectCount).map((title, index) => ({
            id: `project-${index + 1}`,
            title,
            status: index % 2 === 0 ? "active" : "saved",
            updatedAt: `2026-07-28T0${index}:00:00Z`,
          })),
        }),
      });
    }
    if (path === "/api/projects/project-7") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          id: "project-7",
          title: titles[6],
          status: "active",
          updatedAt: "2026-07-28T00:00:00Z",
          messages: [],
          runs: [],
          workflowStatus: "draft",
          stateVersion: 1,
          stateUpdatedAt: "2026-07-28T00:00:00Z",
          responseUpdatedAt: "2026-07-28T00:00:01Z",
          stages: [],
          preview: { version: null, workflowRunId: null },
          consistency: { ok: true, conflictCodes: [] },
        }),
      });
    }
    return route.fulfill({
      status: 404,
      contentType: "application/json",
      body: JSON.stringify({ code: "NOT_FOUND", message: path }),
    });
  });

  await page.goto(baseURL);
  await page.waitForLoadState("networkidle");

  const projectGrid = page.locator('[aria-label="最近项目列表"]');
  await projectGrid.waitFor();
  assert.equal(await projectGrid.getByRole("button").count(), 6);
  assert.equal(await page.getByRole("button", { name: "更多" }).count(), 0);

  projectCount = 7;
  await page.reload();
  await page.waitForLoadState("networkidle");

  const moreButton = page.getByRole("button", { name: "更多" });
  await moreButton.waitFor();
  assert.equal(await moreButton.getAttribute("aria-expanded"), "false");
  assert.equal(
    await moreButton.getAttribute("aria-controls"),
    "recent-projects-grid",
  );
  assert.equal(await projectGrid.getByRole("button").count(), 6);

  const requestsBeforeToggle = projectListRequests;
  await moreButton.focus();
  await moreButton.press("Enter");
  assert.equal(await projectGrid.getByRole("button").count(), 7);
  const collapseButton = page.getByRole("button", { name: "收起" });
  assert.equal(await collapseButton.getAttribute("aria-expanded"), "true");
  assert.equal(
    await collapseButton.evaluate((button) => button === document.activeElement),
    true,
  );
  assert.equal(projectListRequests, requestsBeforeToggle);

  await collapseButton.press("Space");
  assert.equal(await projectGrid.getByRole("button").count(), 6);
  assert.equal(await moreButton.getAttribute("aria-expanded"), "false");
  assert.equal(
    await moreButton.evaluate((button) => button === document.activeElement),
    true,
  );
  assert.equal(projectListRequests, requestsBeforeToggle);

  await moreButton.click();
  assert.equal(await projectGrid.getByRole("button").count(), 7);

  const layout = await page.evaluate(() => {
    const grid = document.querySelector('[aria-label="最近项目列表"]');
    const cards = [...(grid?.querySelectorAll("button") ?? [])];
    const titles = [...(grid?.querySelectorAll("h3") ?? [])];
    return {
      viewportWidth: window.innerWidth,
      scrollWidth: document.documentElement.scrollWidth,
      grid: grid?.getBoundingClientRect().toJSON(),
      cards: cards.map((card) => card.getBoundingClientRect().toJSON()),
      titleStyles: titles.map((title) => {
        const style = getComputedStyle(title);
        return {
          overflow: style.overflow,
          textOverflow: style.textOverflow,
          whiteSpace: style.whiteSpace,
          clientWidth: title.clientWidth,
          scrollWidth: title.scrollWidth,
        };
      }),
    };
  });

  assert.equal(layout.scrollWidth, layout.viewportWidth);
  assert.equal(Math.round(layout.grid.width), 358);
  for (const card of layout.cards) {
    assert.ok(card.left >= 16);
    assert.ok(card.right <= layout.viewportWidth - 16);
    assert.equal(Math.round(card.width), 358);
  }
  for (const index of [0, 1, 2]) {
    const title = layout.titleStyles[index];
    assert.equal(title.overflow, "hidden");
    assert.equal(title.textOverflow, "ellipsis");
    assert.equal(title.whiteSpace, "nowrap");
    assert.ok(title.scrollWidth > title.clientWidth);
  }

  await mkdir("test-results", { recursive: true });
  await page.screenshot({
    path: "test-results/home-responsive-390.png",
    fullPage: true,
  });

  for (const width of [320, 768, 1024]) {
    await page.setViewportSize({ width, height: 900 });
    const responsiveLayout = await page.evaluate(() => {
      const grid = document.querySelector('[aria-label="最近项目列表"]');
      const cards = [...(grid?.querySelectorAll("button") ?? [])];
      return {
        viewportWidth: window.innerWidth,
        scrollWidth: document.documentElement.scrollWidth,
        cards: cards.map((card) => card.getBoundingClientRect().toJSON()),
      };
    });
    assert.equal(responsiveLayout.scrollWidth, responsiveLayout.viewportWidth);
    for (const card of responsiveLayout.cards) {
      assert.ok(card.left >= 0);
      assert.ok(card.right <= responsiveLayout.viewportWidth);
    }
  }
  await page.setViewportSize({ width: 390, height: 844 });

  await page.reload();
  await page.waitForLoadState("networkidle");
  assert.equal(await projectGrid.getByRole("button").count(), 6);
  assert.equal(
    await page.getByRole("button", { name: "更多" }).getAttribute(
      "aria-expanded",
    ),
    "false",
  );

  const consoleErrorsBeforeExpectedFailure = consoleErrors.length;
  projectsResponse = "error";
  await page.reload();
  await page.waitForLoadState("networkidle");
  assert.equal(await page.getByText("最近项目暂时无法加载。").count(), 1);
  assert.equal(await page.getByRole("button", { name: "重新加载" }).count(), 1);
  assert.equal(await page.getByRole("button", { name: "更多" }).count(), 0);
  assert.equal(
    consoleErrors.length,
    consoleErrorsBeforeExpectedFailure + 1,
    "the simulated 503 should be the only expected console error",
  );
  const consoleErrorsAfterExpectedFailure = consoleErrors.length;

  projectsResponse = "success";
  await page.getByRole("button", { name: "重新加载" }).click();
  await moreButton.waitFor();
  assert.equal(await projectGrid.getByRole("button").count(), 6);
  await moreButton.click();
  assert.equal(await projectGrid.getByRole("button").count(), 7);
  const requestsBeforeNavigation = projectListRequests;
  await projectGrid.getByRole("button").nth(6).click();
  await page.waitForURL(`${baseURL}/project/project-7`);
  assert.equal(projectListRequests, requestsBeforeNavigation);
  assert.equal(consoleErrors.length, consoleErrorsAfterExpectedFailure);

  console.log(
    "home responsive e2e passed: 6/7 boundary, keyboard toggle, ARIA/focus, retry, refresh reset, 320/390/768/1024px overflow, seventh-card navigation, no duplicate list request",
  );
} catch (error) {
  console.error(serverOutput);
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
