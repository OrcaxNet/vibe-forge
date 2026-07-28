import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import {
  createBuildInfo,
  manifestDigest,
  sha256,
} from "./build-info.mjs";

const temporaryDirectories = [];

afterEach(async () => {
  await Promise.all(
    temporaryDirectories.splice(0).map((directory) =>
      rm(directory, { recursive: true, force: true }),
    ),
  );
});

describe("frontend build info", () => {
  it("creates a deterministic digest for sorted runtime assets", async () => {
    const directory = await mkdtemp(join(tmpdir(), "vibe-forge-build-info-"));
    temporaryDirectories.push(directory);
    await mkdir(join(directory, "assets"));
    await writeFile(join(directory, "index.html"), "<main>Vibe Forge</main>");
    await writeFile(join(directory, "assets", "app.js"), "console.log('ok')");
    await writeFile(join(directory, "assets", "app.js.map"), "ignored");

    const info = await createBuildInfo(
      directory,
      "a7ee221db8121fff1007f9bebd26ff0e6c58b51e",
      new Date("2026-07-28T05:09:38Z"),
    );

    expect(info).toEqual({
      schemaVersion: 1,
      revision: "a7ee221db8121fff1007f9bebd26ff0e6c58b51e",
      generatedAt: "2026-07-28T05:09:38.000Z",
      assetDigest: manifestDigest(info.assets),
      assets: [
        {
          path: "assets/app.js",
          sha256: sha256("console.log('ok')"),
        },
        {
          path: "index.html",
          sha256: sha256("<main>Vibe Forge</main>"),
        },
      ],
    });
  });

  it("rejects an untraceable production revision", async () => {
    const directory = await mkdtemp(join(tmpdir(), "vibe-forge-build-info-"));
    temporaryDirectories.push(directory);

    await expect(
      createBuildInfo(directory, "main"),
    ).rejects.toThrow(/BUILD_REVISION/);
  });
});
