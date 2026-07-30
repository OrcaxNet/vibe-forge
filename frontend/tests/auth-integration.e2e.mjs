import assert from "node:assert/strict";
import { chromium } from "playwright-core";

const baseURL = process.env.AUTH_E2E_BASE_URL;
const password = process.env.AUTH_E2E_PASSWORD;
const chromePath =
  process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE ??
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";

assert.ok(baseURL, "AUTH_E2E_BASE_URL is required");
assert.ok(password, "AUTH_E2E_PASSWORD is required");

const deepLink = "/project/auth-integration?tab=versions#source";
const deepLinkURL = new URL(deepLink, baseURL).toString();
const browser = await chromium.launch({
  executablePath: chromePath,
  headless: true,
  args: ["--no-sandbox"],
});

try {
  const context = await browser.newContext();
  const protectedBeforeLogin = await context.request.get(
    new URL("/api/projects", baseURL).toString(),
  );
  assert.equal(protectedBeforeLogin.status(), 401);

  const page = await context.newPage();
  await page.goto(deepLinkURL);
  await page.getByRole("heading", { name: "验证访问" }).waitFor();
  assert.equal(page.url(), deepLinkURL);

  await page.getByLabel("访问密码").fill(password);
  await page.getByLabel("访问密码").press("Enter");
  await page.getByRole("button", { name: "退出访问" }).waitFor();
  assert.equal(page.url(), deepLinkURL);

  const cookies = await context.cookies(baseURL);
  const sessionCookie = cookies.find(
    (cookie) => cookie.name === "vf_access_session",
  );
  assert.ok(sessionCookie, "login should set the access-session cookie");
  assert.equal(sessionCookie.httpOnly, true);
  assert.equal(sessionCookie.sameSite, "Lax");
  assert.equal(sessionCookie.secure, false);

  const session = await context.request.get(
    new URL("/api/auth/session", baseURL).toString(),
  );
  assert.equal(session.status(), 200);
  const sessionBody = await session.json();
  assert.equal(sessionBody.authenticated, true);
  assert.ok(sessionBody.expiresAt);

  const protectedAfterLogin = await context.request.get(
    new URL("/api/projects", baseURL).toString(),
  );
  assert.equal(protectedAfterLogin.status(), 200);

  await page.getByRole("button", { name: "退出访问" }).click();
  await page.getByRole("heading", { name: "验证访问" }).waitFor();
  await page.getByText("已退出访问").waitFor();
  assert.equal(page.url(), deepLinkURL);
  assert.equal(
    (await context.cookies(baseURL)).some(
      (cookie) => cookie.name === "vf_access_session",
    ),
    false,
  );
  const sessionAfterLogout = await context.request.get(
    new URL("/api/auth/session", baseURL).toString(),
  );
  assert.equal(sessionAfterLogout.status(), 401);
  await context.close();

  const rateLimitContext = await browser.newContext();
  const loginURL = new URL("/api/auth/login", baseURL).toString();
  for (let attempt = 1; attempt <= 5; attempt += 1) {
    const response = await rateLimitContext.request.post(loginURL, {
      data: { password: `known-wrong-value-${attempt}` },
    });
    assert.equal(response.status(), 401);
  }
  const limited = await rateLimitContext.request.post(loginURL, {
    data: { password: "known-wrong-value-6" },
  });
  assert.equal(limited.status(), 429);
  assert.ok(Number(limited.headers()["retry-after"]) > 0);
  const limitedBody = await limited.json();
  assert.equal(limitedBody.error.code, "AUTH_RATE_LIMITED");
  assert.ok(limitedBody.error.retryAfterSeconds > 0);
  await rateLimitContext.close();

  console.log(
    "auth integration e2e passed: real Cookie, deep link, protected 401/200, logout revocation, and sixth-failure 429",
  );
} finally {
  await browser.close();
}
