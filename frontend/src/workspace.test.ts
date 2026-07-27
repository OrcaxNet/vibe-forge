import { describe, expect, it } from "vitest";
import {
  computeFilesHash,
  createVerifiedPreview,
  isFileEditLocked,
  normalizeFileTree,
  PreviewDeadlineError,
  shouldStagePreview,
  withPreviewDeadline,
  type PreviewSnapshot,
  type VersionView,
} from "./workspace";

const files = {
  "/src/App.tsx": "export default 1",
  "/index.html": "<div />",
};

const stableVersion: VersionView = {
  id: "version-2",
  iterationId: "iteration-2",
  status: "stable",
  filesHash:
    "8fa015fa0134f7e37ae97c1961c15b00adf17a5980019984be81002a01d18f8a",
  createdAt: "2026-07-28T00:00:00Z",
};

describe("workspace version integrity", () => {
  it("matches the Go store path/content SHA-256 algorithm regardless of map order", async () => {
    expect(await computeFilesHash(files)).toBe(stableVersion.filesHash);
    expect(
      await computeFilesHash({
        "/index.html": "<div />",
        "/src/App.tsx": "export default 1",
      }),
    ).toBe(stableVersion.filesHash);
  });

  it("creates a candidate only when version id, stable state, and files hash agree", async () => {
    await expect(createVerifiedPreview(stableVersion, files)).resolves.toEqual({
      versionId: "version-2",
      filesHash: stableVersion.filesHash,
      files,
    });
    await expect(
      createVerifiedPreview(stableVersion, {
        ...files,
        "/src/App.tsx": "broken candidate",
      }),
    ).rejects.toThrow("上一稳定预览");
  });

  it("keeps the current snapshot untouched when a candidate verification fails", async () => {
    const current: PreviewSnapshot = {
      versionId: "version-1",
      filesHash: "current-hash",
      files: { "/src/App.tsx": "working stable app" },
    };

    await expect(
      createVerifiedPreview(stableVersion, {
        "/src/App.tsx": "uncommitted draft",
      }),
    ).rejects.toBeInstanceOf(Error);
    expect(current.versionId).toBe("version-1");
    expect(current.files["/src/App.tsx"]).toBe("working stable app");
  });

  it("deduplicates repeated preview_ready events", () => {
    expect(shouldStagePreview("version-2", undefined, "version-2")).toBe(false);
    expect(shouldStagePreview("version-1", "version-2", "version-2")).toBe(
      false,
    );
    expect(shouldStagePreview("version-1", undefined, "version-2")).toBe(true);
  });

  it("separates validation deadlines from runtime resource deadlines", async () => {
    await expect(
      withPreviewDeadline(
        new Promise<never>(() => undefined),
        "validation_timeout",
        "validation timed out",
        1,
      ),
    ).rejects.toMatchObject({
      name: "PreviewDeadlineError",
      kind: "validation_timeout",
      message: "validation timed out",
    });

    const resourceTimeout = new PreviewDeadlineError(
      "resource_timeout",
      "resource timed out",
    );
    expect(resourceTimeout.kind).toBe("resource_timeout");
  });
});

describe("workspace editing guard", () => {
  it("locks readonly files and all manual saves during an active run", () => {
    expect(isFileEditLocked({ readonly: true }, false)).toBe(true);
    expect(isFileEditLocked({ readonly: false }, true)).toBe(true);
    expect(isFileEditLocked({ readonly: false }, false)).toBe(false);
  });

  it("defaults unknown file entries to readonly", () => {
    const tree = normalizeFileTree({
      files: [
        { path: "/src/App.tsx", content: "app", readonly: false },
        { path: "/src/main.tsx", content: "main" },
      ],
    });
    expect(tree.files).toEqual([
      { path: "/src/App.tsx", content: "app", readonly: false },
      { path: "/src/main.tsx", content: "main", readonly: true },
    ]);
  });
});
