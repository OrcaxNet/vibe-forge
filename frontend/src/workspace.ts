import { WRITABLE_FILE_PATH } from "./contract";

export type FileTreeEntry = {
  path: string;
  content: string;
  readonly: boolean;
};

export type FileTree = {
  stableVersionId?: string;
  files: FileTreeEntry[];
  writableFilePath: string;
};

export type VersionView = {
  id: string;
  iterationId: string;
  status: "draft" | "validating" | "stable" | "failed";
  filesHash?: string;
  createdAt: string;
};

export type ManualIteration = {
  id: string;
  baseVersionId?: string;
  resultVersionId?: string;
};

export type PreviewSnapshot = {
  versionId: string;
  filesHash: string;
  files: Record<string, string>;
};

type UnknownRecord = Record<string, unknown>;

function isRecord(value: unknown): value is UnknownRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

export function normalizeFileTree(value: unknown): FileTree {
  const raw = isRecord(value) ? value : {};
  const files = Array.isArray(raw.files)
    ? raw.files
        .filter(isRecord)
        .map((file) => ({
          path: asString(file.path),
          content: asString(file.content),
          readonly: file.readonly !== false,
        }))
        .filter((file) => file.path.startsWith("/"))
    : [];

  return {
    stableVersionId: asString(raw.stableVersionId) || undefined,
    files,
    writableFilePath:
      asString(raw.writableFilePath) || WRITABLE_FILE_PATH,
  };
}

export function normalizeVersions(value: unknown): VersionView[] {
  const items = Array.isArray(value)
    ? value
    : isRecord(value) && Array.isArray(value.versions)
      ? value.versions
      : [];

  return items
    .filter(isRecord)
    .map((version) => ({
      id: asString(version.id),
      iterationId: asString(version.iterationId),
      status: asString(version.status) as VersionView["status"],
      filesHash: asString(version.filesHash) || undefined,
      createdAt: asString(version.createdAt),
    }))
    .filter(
      (version) =>
        Boolean(version.id) &&
        ["draft", "validating", "stable", "failed"].includes(version.status),
    )
    .sort(
      (left, right) =>
        new Date(right.createdAt).getTime() -
        new Date(left.createdAt).getTime(),
    );
}

export function normalizeFilesMap(value: unknown): Record<string, string> {
  if (!isRecord(value)) return {};
  const files: Record<string, string> = {};
  Object.entries(value)
    .filter(
      (entry): entry is [string, string] =>
        entry[0].startsWith("/") && typeof entry[1] === "string",
    )
    .sort(([left], [right]) => (left < right ? -1 : left > right ? 1 : 0))
    .forEach(([path, content]) => {
      files[path] = content;
    });
  return files;
}

export async function computeFilesHash(
  files: Record<string, string>,
): Promise<string> {
  const encoder = new TextEncoder();
  const chunks = Object.entries(files)
    .sort(([left], [right]) => (left < right ? -1 : left > right ? 1 : 0))
    .flatMap(([path, content]) => [
      encoder.encode(path),
      new Uint8Array([0]),
      encoder.encode(content),
      new Uint8Array([0]),
    ]);
  const length = chunks.reduce((total, chunk) => total + chunk.byteLength, 0);
  const payload = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    payload.set(chunk, offset);
    offset += chunk.byteLength;
  }

  const digest = await crypto.subtle.digest("SHA-256", payload);
  return Array.from(new Uint8Array(digest))
    .map((byte) => byte.toString(16).padStart(2, "0"))
    .join("");
}

export async function createVerifiedPreview(
  version: VersionView,
  files: Record<string, string>,
): Promise<PreviewSnapshot> {
  if (version.status !== "stable" || !version.filesHash) {
    throw new Error("版本尚未完成稳定提交。");
  }
  const actualHash = await computeFilesHash(files);
  if (actualHash !== version.filesHash) {
    throw new Error("版本文件校验失败，已继续保留上一稳定预览。");
  }
  return { versionId: version.id, filesHash: actualHash, files };
}

export function shouldStagePreview(
  visibleVersionId: string | undefined,
  pendingVersionId: string | undefined,
  nextVersionId: string,
): boolean {
  return (
    Boolean(nextVersionId) &&
    nextVersionId !== visibleVersionId &&
    nextVersionId !== pendingVersionId
  );
}

export function isFileEditLocked(
  file: Pick<FileTreeEntry, "readonly"> | undefined,
  activeRun: boolean,
): boolean {
  return !file || file.readonly || activeRun;
}

export function sandpackFiles(
  files: Record<string, string>,
): Record<string, { code: string; readOnly: boolean; hidden: boolean }> {
  return Object.fromEntries(
    Object.entries(files).map(([path, code]) => [
      path,
      {
        code,
        readOnly: path !== WRITABLE_FILE_PATH,
        hidden: path !== WRITABLE_FILE_PATH,
      },
    ]),
  );
}
