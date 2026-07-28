import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdir } from "node:fs/promises";
import { chromium } from "playwright-core";

const baseURL = "http://127.0.0.1:5173";
const chromePath =
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";

const titles = [
  "FLO-78：做一个点击计数器，支持加一、减一和重置，界面简洁。",
  "this-is-a-deliberately-long-unbroken-english-project-title-that-must-not-expand-the-card",
  "123e4567-e89b-12d3-a456-426614174000",
  "做一个待办清单，支持新增、完成、删除和清空已完成。",
  "计数器 Demo",
  "生成一个食谱收藏页，支持新增食谱、关键词筛选和删除。",
];

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
    if (path === "/api/health") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ status: "healthy" }),
      });
    }
    if (path === "/api/projects") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          projects: titles.map((title, index) => ({
            id: `project-${index + 1}`,
            title,
            status: index % 2 === 0 ? "active" : "saved",
            updatedAt: `2026-07-28T0${index}:00:00Z`,
          })),
        }),
      });
    }
    if (path === "/api/projects/project-1") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          id: "project-1",
          title: titles[0],
          status: "active",
          updatedAt: "2026-07-28T00:00:00Z",
          messages: [],
          runs: [],
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
  const cards = projectGrid.getByRole("button");
  assert.equal(await cards.count(), 6);

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

  await cards.first().click();
  await page.waitForURL(`${baseURL}/project/project-1`);
  assert.equal(consoleErrors.length, 0);

  console.log(
    "home responsive e2e passed: 390px, six cards, CJK/unbroken/UUID truncation, no horizontal overflow, card navigation",
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
