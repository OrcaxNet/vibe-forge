import { createHash } from "node:crypto";
import { readdir, readFile } from "node:fs/promises";
import { relative, resolve, sep } from "node:path";

export const REVISION_PATTERN = /^(?:local|unknown|[0-9a-f]{7,40})$/;

export function sha256(content) {
  return createHash("sha256").update(content).digest("hex");
}

export function manifestDigest(assets) {
  const canonical = assets
    .map(({ path, sha256: digest }) => `${path}\0${digest}\n`)
    .join("");
  return sha256(canonical);
}

async function listRuntimeFiles(root, directory = root) {
  const entries = await readdir(directory, { withFileTypes: true });
  const files = [];

  for (const entry of entries) {
    const absolutePath = resolve(directory, entry.name);
    if (entry.isDirectory()) {
      files.push(...(await listRuntimeFiles(root, absolutePath)));
      continue;
    }

    const path = relative(root, absolutePath).split(sep).join("/");
    if (
      entry.isFile() &&
      path !== "build-info.json" &&
      !path.endsWith(".map")
    ) {
      files.push({ absolutePath, path });
    }
  }

  return files.sort((left, right) => left.path.localeCompare(right.path));
}

export async function createBuildInfo(
  distDirectory,
  revision,
  generatedAt = new Date(),
) {
  if (!REVISION_PATTERN.test(revision)) {
    throw new Error(
      "BUILD_REVISION must be local, unknown, or a 7-40 character lowercase Git SHA",
    );
  }

  const root = resolve(distDirectory);
  const files = await listRuntimeFiles(root);
  const assets = await Promise.all(
    files.map(async ({ absolutePath, path }) => ({
      path,
      sha256: sha256(await readFile(absolutePath)),
    })),
  );

  return {
    schemaVersion: 1,
    revision,
    generatedAt: generatedAt.toISOString(),
    assetDigest: manifestDigest(assets),
    assets,
  };
}
