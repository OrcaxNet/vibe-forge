import { spawn } from "node:child_process";
import { once } from "node:events";
import { fileURLToPath } from "node:url";
import { createServer } from "node:net";

const host = "127.0.0.1";
const viteBin = fileURLToPath(
  new URL("../node_modules/vite/bin/vite.js", import.meta.url),
);

function configuredPort(envName) {
  const value = process.env[envName];
  if (!value) return undefined;
  const port = Number(value);
  if (!Number.isInteger(port) || port < 1 || port > 65_535) {
    throw new Error(`${envName} must be an integer between 1 and 65535`);
  }
  return port;
}

async function availablePort() {
  const probe = createServer();
  await new Promise((resolve, reject) => {
    probe.once("error", reject);
    probe.listen(0, host, resolve);
  });
  const address = probe.address();
  const port =
    typeof address === "object" && address !== null ? address.port : undefined;
  await new Promise((resolve, reject) => {
    probe.close((error) => (error ? reject(error) : resolve()));
  });
  if (!port) throw new Error("failed to allocate an E2E port");
  return port;
}

export async function startViteTestServer({ name, portEnv }) {
  const port = configuredPort(portEnv) ?? (await availablePort());
  const baseURL = `http://${host}:${port}`;
  const child = spawn(
    process.execPath,
    [viteBin, "--host", host, "--port", String(port), "--strictPort"],
    {
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  let output = "";
  let spawnError;
  child.stdout.on("data", (chunk) => {
    output += chunk.toString();
  });
  child.stderr.on("data", (chunk) => {
    output += chunk.toString();
  });
  child.once("error", (error) => {
    spawnError = error;
  });
  console.log(
    `[vite-test-server] ${name}: ${baseURL} (--strictPort, override with ${portEnv})`,
  );
  return {
    baseURL,
    child,
    output: () => output,
    ready: () => output.includes(`${baseURL}/`),
    spawnError: () => spawnError,
  };
}

export async function waitForViteTestServer(server, timeoutMs = 20_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (server.spawnError()) throw server.spawnError();
    if (server.child.exitCode !== null) {
      throw new Error(
        `Vite exited with code ${server.child.exitCode} before becoming ready\n${server.output()}`,
      );
    }
    // A fetch alone is insufficient: another deployment may already own the
    // selected port. Only probe after this exact Vite child advertised its URL.
    if (!server.ready()) {
      await new Promise((resolve) => setTimeout(resolve, 150));
      continue;
    }
    try {
      const response = await fetch(server.baseURL);
      if (response.ok) return;
    } catch {
      // Vite is still starting.
    }
    await new Promise((resolve) => setTimeout(resolve, 150));
  }
  throw new Error(
    `Vite did not start within ${timeoutMs}ms at ${server.baseURL}\n${server.output()}`,
  );
}

export async function stopViteTestServer(server, timeoutMs = 2_000) {
  const child = server.child;
  if (child.exitCode !== null || child.signalCode !== null) return;

  const exited = once(child, "exit").then(() => true);
  child.kill("SIGTERM");
  const stopped = await Promise.race([
    exited,
    new Promise((resolve) => setTimeout(() => resolve(false), timeoutMs)),
  ]);
  if (stopped) return;

  const forceExited = once(child, "exit");
  child.kill("SIGKILL");
  await forceExited;
}
