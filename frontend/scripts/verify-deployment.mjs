import { manifestDigest, REVISION_PATTERN, sha256 } from "./build-info.mjs";

function assertManifest(manifest) {
  if (
    manifest?.schemaVersion !== 1 ||
    !REVISION_PATTERN.test(manifest.revision ?? "") ||
    !/^[0-9a-f]{64}$/.test(manifest.assetDigest ?? "") ||
    !Array.isArray(manifest.assets) ||
    manifest.assets.length === 0
  ) {
    throw new Error("build-info.json has an invalid schema");
  }

  for (const asset of manifest.assets) {
    if (
      typeof asset?.path !== "string" ||
      asset.path.startsWith("/") ||
      asset.path.includes("..") ||
      asset.path.includes("://") ||
      !/^[0-9a-f]{64}$/.test(asset.sha256 ?? "")
    ) {
      throw new Error("build-info.json contains an invalid asset entry");
    }
  }
}

export async function verifyDeployment(
  publicUrl,
  expectedRevision,
  fetchImpl = fetch,
) {
  if (!REVISION_PATTERN.test(expectedRevision)) {
    throw new Error("Expected revision must be a lowercase Git SHA");
  }

  const baseUrl = new URL(publicUrl);
  baseUrl.pathname = baseUrl.pathname.replace(/\/?$/, "/");
  const manifestUrl = new URL("build-info.json", baseUrl);
  manifestUrl.searchParams.set("_revision", expectedRevision);

  const manifestResponse = await fetchImpl(manifestUrl, {
    cache: "no-store",
    headers: { accept: "application/json" },
  });
  if (!manifestResponse.ok) {
    throw new Error(
      `build-info.json returned HTTP ${manifestResponse.status}`,
    );
  }

  const manifest = await manifestResponse.json();
  assertManifest(manifest);
  if (manifest.revision !== expectedRevision) {
    throw new Error(
      `deployed revision ${manifest.revision} does not match ${expectedRevision}`,
    );
  }

  for (const asset of manifest.assets) {
    const assetUrl = new URL(asset.path, baseUrl);
    assetUrl.searchParams.set("_revision", expectedRevision);
    const response = await fetchImpl(assetUrl, { cache: "no-store" });
    if (!response.ok) {
      throw new Error(`${asset.path} returned HTTP ${response.status}`);
    }

    const actualDigest = sha256(Buffer.from(await response.arrayBuffer()));
    if (actualDigest !== asset.sha256) {
      throw new Error(
        `${asset.path} digest ${actualDigest} does not match ${asset.sha256}`,
      );
    }
  }

  const actualManifestDigest = manifestDigest(manifest.assets);
  if (actualManifestDigest !== manifest.assetDigest) {
    throw new Error(
      `manifest digest ${actualManifestDigest} does not match ${manifest.assetDigest}`,
    );
  }

  return manifest;
}

if (process.argv[1]?.endsWith("verify-deployment.mjs")) {
  const publicUrl = process.argv[2];
  const expectedRevision = process.argv[3];
  if (!publicUrl || !expectedRevision) {
    console.error(
      "Usage: npm run verify:deployment -- <public-url> <expected-git-revision>",
    );
    process.exit(2);
  }

  try {
    const manifest = await verifyDeployment(publicUrl, expectedRevision);
    console.log(
      `Verified ${manifest.revision} at ${publicUrl}: ${manifest.assets.length} assets, digest ${manifest.assetDigest}`,
    );
  } catch (error) {
    console.error(
      `Deployment verification failed: ${
        error instanceof Error ? error.message : String(error)
      }`,
    );
    process.exit(1);
  }
}
