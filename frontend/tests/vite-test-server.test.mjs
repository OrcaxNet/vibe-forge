import { createServer } from "node:http";
import { afterEach, describe, expect, it } from "vitest";
import {
  startViteTestServer,
  stopViteTestServer,
  waitForViteTestServer,
} from "./vite-test-server.mjs";

const portEnv = "VITE_TEST_SERVER_TEST_PORT";
const activeServers = new Set();

afterEach(async () => {
  delete process.env[portEnv];
  await Promise.all(
    [...activeServers].map(async (server) => {
      await stopViteTestServer(server);
      activeServers.delete(server);
    }),
  );
});

describe("Vite E2E server isolation", () => {
  it("starts on an independent port and waits for its process to exit", async () => {
    const server = await startViteTestServer({
      name: "lifecycle-test",
      portEnv,
    });
    activeServers.add(server);

    await waitForViteTestServer(server);
    const response = await fetch(server.baseURL);
    expect(response.ok).toBe(true);

    await stopViteTestServer(server);
    activeServers.delete(server);
    expect(
      server.child.exitCode !== null || server.child.signalCode !== null,
    ).toBe(true);
  });

  it("rejects an occupied strict port even when the incumbent returns HTTP 200", async () => {
    const incumbent = createServer((_request, response) => {
      response.writeHead(200, { "content-type": "text/plain" });
      response.end("not the test deployment");
    });
    await new Promise((resolve, reject) => {
      incumbent.once("error", reject);
      incumbent.listen(0, "127.0.0.1", resolve);
    });
    const address = incumbent.address();
    if (typeof address !== "object" || address === null) {
      throw new Error("incumbent server has no TCP address");
    }
    process.env[portEnv] = String(address.port);

    try {
      const server = await startViteTestServer({
        name: "strict-port-test",
        portEnv,
      });
      activeServers.add(server);
      await expect(waitForViteTestServer(server)).rejects.toThrow(
        /Vite exited with code 1 before becoming ready/,
      );
    } finally {
      await new Promise((resolve, reject) => {
        incumbent.close((error) => (error ? reject(error) : resolve()));
      });
    }
  });
});
