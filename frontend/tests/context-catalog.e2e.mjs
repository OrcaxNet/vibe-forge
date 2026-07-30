import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdir } from "node:fs/promises";
import { chromium } from "playwright-core";

const baseURL = "http://127.0.0.1:5173";
const chromePath =
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";

async function waitForServer() {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`${baseURL}/context-catalog`);
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
  const page = await browser.newPage({ viewport: { width: 1440, height: 960 } });
  const consoleErrors = [];
  const failedResponses = [];
  page.on("console", (message) => {
    if (message.type() === "error") {
      consoleErrors.push(
        `${message.text()} @ ${JSON.stringify(message.location())}`,
      );
    }
  });
  page.on("response", (response) => {
    if (response.status() >= 400) {
      failedResponses.push(`${response.status()} ${response.url()}`);
    }
  });

  await page.goto(`${baseURL}/context-catalog`);
  await page.waitForLoadState("networkidle");

  await page.getByRole("heading", { name: "search-api" }).waitFor();
  assert.equal(
    await page.getByText("24 components", { exact: true }).count(),
    1,
  );
  assert.match(
    await page.getByTestId("rendered-content").textContent(),
    /how to reset 2FA/,
  );
  assert.match(await page.locator("body").innerText(), /\d+ tokens/);

  const search = page.getByRole("textbox", {
    name: "Search context components",
  });
  await search.fill("retrieval");
  assert.equal(await page.getByRole("button", { name: /search-api/ }).count(), 1);
  assert.equal(
    await page.getByRole("button", { name: /vector-search/ }).count(),
    1,
  );
  assert.equal(
    await page.getByRole("button", { name: /db-query-tool/ }).count(),
    0,
  );
  const timing = await page.getByTestId("search-timing").textContent();
  const elapsed = Number(timing?.match(/([\d.]+) ms/)?.[1]);
  assert.ok(Number.isFinite(elapsed));
  assert.ok(elapsed < 500, `catalog search took ${elapsed}ms`);

  await search.fill("");
  await page.getByRole("button", { name: "multilingual" }).click();
  assert.match(
    await page.getByTestId("rendered-content").textContent(),
    /如何重置两步验证/,
  );

  const injectionStoryButton = page.getByRole("button", {
    name: "injection-attempt",
  });
  await injectionStoryButton.click();
  const preview = page.getByTestId("rendered-content");
  assert.match(await preview.textContent(), /<img src=x onerror=/);
  assert.equal(await preview.locator("img").count(), 0);
  assert.equal(
    await page.evaluate(() => window.__ccddExecuted),
    undefined,
  );
  await page.waitForFunction(() => {
    const button = [...document.querySelectorAll("button")].find(
      (candidate) => candidate.textContent?.trim() === "injection-attempt",
    );
    return (
      button &&
      getComputedStyle(button).backgroundColor === "rgb(29, 106, 89)"
    );
  });
  const injectionStoryColor = await injectionStoryButton.evaluate(
    (button) => getComputedStyle(button).backgroundColor,
  );
  const defaultStoryColor = await page
    .getByRole("button", { name: "default", exact: true })
    .evaluate((button) => getComputedStyle(button).backgroundColor);
  assert.equal(injectionStoryColor, "rgb(29, 106, 89)");
  assert.notEqual(
    defaultStoryColor,
    injectionStoryColor,
  );
  assert.match(await page.locator("body").innerText(), /injection markers? detected/i);

  await mkdir("test-results", { recursive: true });
  await page.screenshot({
    path: "test-results/context-catalog-1440.png",
    fullPage: true,
  });

  await page.setViewportSize({ width: 390, height: 844 });
  const responsive = await page.evaluate(() => ({
    viewportWidth: window.innerWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }));
  assert.equal(responsive.scrollWidth, responsive.viewportWidth);
  assert.equal(
    consoleErrors.length,
    0,
    [...consoleErrors, ...failedResponses].join("\n"),
  );

  console.log(
    "context catalog e2e passed: 24-item search, <500ms timing, canonical stories, inert injection payload, token display, desktop/mobile layout",
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
