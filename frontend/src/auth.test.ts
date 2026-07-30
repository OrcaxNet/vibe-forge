import { describe, expect, it, vi } from "vitest";
import {
  AUTH_PATHS,
  isAuthApiPath,
  parseAuthFeedback,
  retryAfterSeconds,
} from "./auth";

describe("auth response helpers", () => {
  it("normalizes the nested authentication error contract", () => {
    expect(
      parseAuthFeedback({
        error: {
          code: "AUTH_RATE_LIMITED",
          message: "尝试次数过多",
          retryAfterSeconds: 1.2,
        },
      }),
    ).toEqual({
      code: "AUTH_RATE_LIMITED",
      message: "尝试次数过多",
      retryAfterSeconds: 2,
    });
  });

  it("accepts Retry-After seconds and dates without returning zero", () => {
    expect(retryAfterSeconds({}, "3.2")).toBe(4);

    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-30T08:00:00Z"));
    expect(retryAfterSeconds({}, "Thu, 30 Jul 2026 08:00:02 GMT")).toBe(2);
    vi.useRealTimers();
  });

  it("keeps authentication 401s separate from protected API expiry", () => {
    expect(isAuthApiPath(AUTH_PATHS.session)).toBe(true);
    expect(isAuthApiPath(`${AUTH_PATHS.login}?next=%2Fproject%2F1`)).toBe(true);
    expect(isAuthApiPath("/api/projects/project-1")).toBe(false);
  });
});
