import { writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { createBuildInfo } from "./build-info.mjs";

const distDirectory = resolve(process.argv[2] ?? "dist");
const revision = process.env.BUILD_REVISION ?? "unknown";
const buildInfo = await createBuildInfo(distDirectory, revision);

await writeFile(
  resolve(distDirectory, "build-info.json"),
  `${JSON.stringify(buildInfo)}\n`,
  "utf8",
);

console.log(
  `Wrote build-info.json for ${buildInfo.revision} (${buildInfo.assetDigest})`,
);
