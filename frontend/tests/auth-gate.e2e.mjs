import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { chromium } from "playwright-core";
import { startViteTestServer } from "./vite-test-server.mjs";

const testServer = await startViteTestServer(41730);
const { baseURL } = testServer;
const chromePath =
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const targetPath = "/project/project-1/files?tab=versions#source";
const targetURL = `${baseURL}${targetPath}`;

let browser;
try {
  browser = await chromium.launch({
    executablePath: chromePath,
    headless: true,
    args: ["--no-sandbox"],
  });
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } });

  let authenticated = false;
  let delayInitialSession = true;
  let releaseInitialSession;
  let loginMode = "invalid";
  let releaseDelayedLogin;
  let protectedRequestUnauthorized = false;
  let loginRequests = 0;
  let logoutRequests = 0;
  const loginBodies = [];

  await page.context().route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const json = (body, status = 200, headers = {}) =>
      route.fulfill({
        status,
        headers,
        contentType: "application/json",
        body: JSON.stringify(body),
      });

    if (path === "/api/auth/session") {
      if (delayInitialSession) {
        await new Promise((resolve) => {
          releaseInitialSession = resolve;
        });
        delayInitialSession = false;
      }
      return authenticated
        ? json({
            authenticated: true,
            expiresAt: "2026-07-30T20:00:00Z",
          })
        : json(
            {
              error: {
                code: "AUTH_REQUIRED",
                message: "需要访问验证",
                retryAfterSeconds: 0,
              },
            },
            401,
          );
    }

    if (path === "/api/auth/login") {
      loginRequests += 1;
      loginBodies.push(request.postDataJSON());
      if (loginMode === "delayed") {
        await new Promise((resolve) => {
          releaseDelayedLogin = resolve;
        });
        return json(
          {
            error: {
              code: "AUTH_INVALID",
              message: "密码错误，请重试",
              retryAfterSeconds: 0,
            },
          },
          401,
        );
      }
      if (loginMode === "limited") {
        return json(
          {
            error: {
              code: "AUTH_RATE_LIMITED",
              message: "尝试次数过多",
              retryAfterSeconds: 2,
            },
          },
          429,
          { "Retry-After": "2" },
        );
      }
      if (loginMode === "success") {
        authenticated = true;
        return route.fulfill({ status: 204, body: "" });
      }
      if (loginMode === "unavailable") {
        return json(
          {
            error: {
              code: "AUTH_CONFIG_UNAVAILABLE",
              message: "internal configuration detail",
              retryAfterSeconds: 0,
            },
          },
          503,
        );
      }
      return json(
        {
          error: {
            code: "AUTH_INVALID",
            message: "密码错误，请重试",
            retryAfterSeconds: 0,
          },
        },
        401,
      );
    }

    if (path === "/api/auth/logout") {
      logoutRequests += 1;
      authenticated = false;
      return route.fulfill({ status: 204, body: "" });
    }

    if (path === "/api/health") return json({ status: "healthy" });

    if (path === "/api/projects/project-1") {
      if (protectedRequestUnauthorized) {
        return json(
          {
            error: {
              code: "AUTH_REQUIRED",
              message: "需要访问验证",
              retryAfterSeconds: 0,
            },
          },
          401,
        );
      }
      return json(
        {
          code: "PROJECT_LOADING",
          message: "项目正在恢复",
          retryable: true,
        },
        503,
      );
    }

    return json(
      {
        code: "NOT_FOUND",
        message: `unmocked ${request.method()} ${path}`,
      },
      404,
    );
  });

  await page.goto(targetURL);
  await page
    .getByRole("heading", { name: "正在检查访问状态" })
    .waitFor();
  assert.equal(
    await page.getByRole("heading", { name: "把一个想法，" }).count(),
    0,
    "business content must not render while the session request is pending",
  );
  releaseInitialSession();

  const gateHeading = page.getByRole("heading", { name: "访问验证" });
  await gateHeading.waitFor();
  assert.equal(page.url(), targetURL);
  assert.equal(await page.getByText("开始构建").count(), 0);

  const password = page.getByLabel("访问密码");
  const enter = page.getByRole("button", { name: "进入" });
  assert.equal(await password.getAttribute("type"), "password");
  assert.equal(await enter.isDisabled(), true);
  await password.press("Enter");
  assert.equal(loginRequests, 0);
  assert.match(
    await password.getAttribute("aria-describedby"),
    /access-password-help/,
  );

  await page.getByRole("button", { name: "显示密码" }).click();
  assert.equal(await password.getAttribute("type"), "text");
  assert.equal(
    await page
      .getByRole("button", { name: "隐藏密码" })
      .getAttribute("aria-pressed"),
    "true",
  );
  await page.getByRole("button", { name: "隐藏密码" }).click();
  assert.equal(await password.getAttribute("type"), "password");

  for (const width of [320, 390, 768, 1024]) {
    await page.setViewportSize({ width, height: 760 });
    const layout = await page.evaluate(() => ({
      viewportWidth: window.innerWidth,
      scrollWidth: document.documentElement.scrollWidth,
      input: document
        .querySelector("#access-password")
        ?.getBoundingClientRect()
        .toJSON(),
      button: document
        .querySelector('button[type="submit"]')
        ?.getBoundingClientRect()
        .toJSON(),
    }));
    assert.equal(layout.scrollWidth, layout.viewportWidth);
    assert.ok(layout.input.left >= 0 && layout.input.right <= width);
    assert.ok(layout.button.left >= 0 && layout.button.right <= width);
  }
  await page.setViewportSize({ width: 390, height: 520 });
  await password.focus();
  const compactViewport = await page.evaluate(() => ({
    height: window.innerHeight,
    input: document
      .querySelector("#access-password")
      ?.getBoundingClientRect()
      .toJSON(),
    button: document
      .querySelector('button[type="submit"]')
      ?.getBoundingClientRect()
      .toJSON(),
  }));
  assert.ok(compactViewport.input.top >= 0);
  assert.ok(compactViewport.button.bottom <= compactViewport.height);
  await page.setViewportSize({ width: 390, height: 844 });

  await mkdir("test-results", { recursive: true });
  await page.screenshot({
    path: "test-results/auth-gate-390.png",
    fullPage: true,
  });

  await password.fill(" wrong ");
  await password.press("Enter");
  const invalidError = page.getByRole("alert").getByText("密码错误，请重试");
  await invalidError.waitFor();
  assert.deepEqual(loginBodies.at(-1), { password: " wrong " });
  await page.waitForFunction(
    () => document.activeElement?.id === "access-password",
  );
  assert.equal(
    await password.evaluate((element) => element === document.activeElement),
    true,
  );

  loginMode = "delayed";
  await password.fill("single-request");
  const requestsBeforeDelayed = loginRequests;
  await password.press("Enter");
  await page.getByRole("button", { name: "验证中…" }).waitFor();
  await page.keyboard.press("Enter");
  await page.waitForTimeout(100);
  assert.equal(loginRequests, requestsBeforeDelayed + 1);
  assert.equal(await password.isDisabled(), true);
  releaseDelayedLogin();
  await invalidError.waitFor();

  loginMode = "unavailable";
  await password.fill("service-unavailable");
  await password.press("Enter");
  await page.getByRole("alert").getByText("暂时无法验证，请稍后重试").waitFor();
  assert.equal(
    await page.getByText("internal configuration detail").count(),
    0,
    "server configuration details must not be exposed by the gate",
  );

  loginMode = "limited";
  await password.fill("rate-limited");
  await password.press("Enter");
  await page.getByText("尝试次数过多，请在 2 秒后重试").waitFor();
  assert.equal(await enter.isDisabled(), true);
  await page.getByText("尝试次数过多，请在 1 秒后重试").waitFor();
  await page.waitForFunction(
    () => !document.querySelector('button[type="submit"]')?.disabled,
    undefined,
    { timeout: 4_000 },
  );

  loginMode = "success";
  await password.fill("correct-for-test-only");
  await password.press("Enter");
  const logout = page.getByRole("button", { name: "退出访问" });
  await logout.waitFor();
  assert.equal(page.url(), targetURL);

  protectedRequestUnauthorized = true;
  await page.reload();
  await page.getByText("访问已过期，请重新验证").waitFor();
  assert.equal(page.url(), targetURL);
  await page.setViewportSize({ width: 1024, height: 900 });
  await page.screenshot({
    path: "test-results/auth-expired-1024.png",
    fullPage: true,
  });

  protectedRequestUnauthorized = false;
  loginMode = "success";
  await password.fill("correct-for-test-only");
  await password.press("Enter");
  await logout.waitFor();
  await logout.click();
  await gateHeading.waitFor();
  await page.getByText("已退出访问").waitFor();
  assert.equal(logoutRequests, 1);

  await page.reload();
  await gateHeading.waitFor();
  assert.equal(page.url(), targetURL);
  assert.equal(await page.getByRole("button", { name: "进入" }).isDisabled(), true);

  console.log(
    "auth gate e2e passed: no content flash, raw password submit, Enter, visibility, duplicate guard, 401, 429 cooldown, deep-link restore, expiry, logout, ARIA, 320/390/768/1024px",
  );
} catch (error) {
  console.error(testServer.output());
  throw error;
} finally {
  if (browser) await browser.close();
  await testServer.stop();
}
