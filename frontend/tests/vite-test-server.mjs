import { spawn } from "node:child_process";
import { once } from "node:events";

const host = "127.0.0.1";

function wait(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

export async function startViteTestServer(port) {
  const baseURL = `http://${host}:${port}`;
  const server = spawn(
    process.execPath,
    [
      "node_modules/vite/bin/vite.js",
      "--host",
      host,
      "--port",
      String(port),
      "--strictPort",
    ],
    { stdio: ["ignore", "pipe", "pipe"] },
  );
  let output = "";
  server.stdout.on("data", (chunk) => {
    output += chunk.toString();
  });
  server.stderr.on("data", (chunk) => {
    output += chunk.toString();
  });

  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    if (server.exitCode !== null) {
      throw new Error(
        `Vite exited before ${baseURL} was ready (code ${server.exitCode}).\n${output}`,
      );
    }
    try {
      const response = await fetch(baseURL);
      if (response.ok) {
        return {
          baseURL,
          output: () => output,
          async stop() {
            if (server.exitCode !== null) return;
            const exited = once(server, "exit");
            server.kill("SIGTERM");
            if (
              await Promise.race([
                exited.then(() => true),
                wait(2_000).then(() => false),
              ])
            ) {
              return;
            }
            server.kill("SIGKILL");
            await exited;
          },
        };
      }
    } catch {
      // Vite is still starting.
    }
    await wait(150);
  }

  const exited = once(server, "exit");
  server.kill("SIGTERM");
  await exited;
  throw new Error(`Vite did not start at ${baseURL} within 20 seconds.\n${output}`);
}
