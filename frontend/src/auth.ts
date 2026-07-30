export const AUTH_PATHS = {
  session: "/api/auth/session",
  login: "/api/auth/login",
  logout: "/api/auth/logout",
} as const;

export const AUTH_EXPIRED_EVENT = "vibe-forge:auth-expired";

type JsonObject = Record<string, unknown>;

export type AuthFeedback = {
  code: string;
  message: string;
  retryAfterSeconds: number;
};

function isObject(value: unknown): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function asPositiveSeconds(value: unknown): number {
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) {
    return 0;
  }
  return Math.ceil(value);
}

export function parseAuthFeedback(body: unknown): AuthFeedback {
  const root = isObject(body) ? body : {};
  const error = isObject(root.error) ? root.error : root;
  return {
    code: typeof error.code === "string" ? error.code : "AUTH_UNAVAILABLE",
    message:
      typeof error.message === "string" && error.message.length > 0
        ? error.message
        : "暂时无法验证，请稍后重试",
    retryAfterSeconds: asPositiveSeconds(error.retryAfterSeconds),
  };
}

export function retryAfterSeconds(
  body: unknown,
  retryAfterHeader: string | null,
): number {
  const bodySeconds = parseAuthFeedback(body).retryAfterSeconds;
  if (bodySeconds > 0) return bodySeconds;

  if (!retryAfterHeader) return 60;
  const seconds = Number(retryAfterHeader);
  if (Number.isFinite(seconds) && seconds > 0) return Math.ceil(seconds);

  const date = Date.parse(retryAfterHeader);
  if (Number.isNaN(date)) return 60;
  return Math.max(1, Math.ceil((date - Date.now()) / 1_000));
}

export function isAuthApiPath(path: string): boolean {
  return Object.values(AUTH_PATHS).some((authPath) => path.startsWith(authPath));
}

export function notifyAuthExpired(): void {
  window.dispatchEvent(new Event(AUTH_EXPIRED_EVENT));
}
