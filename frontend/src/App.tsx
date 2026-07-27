// Vibe Forge workbench - Stage 1 skeleton.
//
// This is the only business component in the Stage 1 frontend skeleton. Stage 2
// (FLO-54) replaces it with the real home page, project workspace and
// Build Pulse. It deliberately reads the four stage names, the writable file
// path and the contract version from the shared contract so the frontend is
// proven to consume the same contract as the backend.
import { useEffect, useState } from "react";
import {
  STAGES,
  WRITABLE_FILE_PATH,
  contractVersion,
  PROMPT_MAX_CHARS,
  type StageNodeStatus,
} from "./contract";

type Health = {
  status: string;
  contractVersion: string;
  version: string;
  dependencies: {
    database: { status: string };
    model: { status: string };
  };
};

export default function App() {
  const [health, setHealth] = useState<Health | null>(null);
  const [healthError, setHealthError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    fetch("/api/health")
      .then(async (r) => {
        const body = (await r.json()) as Health;
        if (!cancelled) setHealth(body);
      })
      .catch((e: unknown) => {
        if (!cancelled) setHealthError(String(e));
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <main className="min-h-screen bg-slate-50 text-slate-900 p-6 dark:bg-slate-950 dark:text-slate-100">
      <header className="max-w-3xl mx-auto">
        <h1 className="text-2xl font-bold">Vibe Forge</h1>
        <p className="text-sm text-slate-500 dark:text-slate-400">
          Stage 1 skeleton · contract v{contractVersion} · writable file{" "}
          <code className="font-mono">{WRITABLE_FILE_PATH}</code> · prompt max{" "}
          {PROMPT_MAX_CHARS} chars
        </p>
      </header>

      <section
        aria-label="Build Pulse stages"
        className="max-w-3xl mx-auto mt-6 grid grid-cols-2 gap-3 sm:grid-cols-4"
      >
        {STAGES.map((stage, i) => {
          const nodeStatus: StageNodeStatus = "pending";
          return (
            <div
              key={stage}
              className="rounded-lg border border-slate-200 dark:border-slate-800 p-3"
            >
              <div className="text-xs uppercase tracking-wide text-slate-400">
                Stage {i + 1}
              </div>
              <div className="font-mono font-semibold">{stage}</div>
              <div className="text-xs text-slate-500" aria-live="polite">
                {nodeStatus}
              </div>
            </div>
          );
        })}
      </section>

      <section
        aria-label="Backend health"
        className="max-w-3xl mx-auto mt-6 rounded-lg border border-slate-200 dark:border-slate-800 p-4 text-sm"
      >
        <h2 className="font-semibold mb-2">Backend health</h2>
        {healthError && (
          <p role="alert" className="text-red-600">
            unable to reach backend: {healthError}
          </p>
        )}
        {health && (
          <ul className="space-y-1 font-mono text-xs">
            <li>status: {health.status}</li>
            <li>backend: {health.version}</li>
            <li>
              contract: {health.contractVersion}{" "}
              {health.contractVersion === contractVersion ? "✓" : "✗ drift"}
            </li>
            <li>database: {health.dependencies.database.status}</li>
            <li>model: {health.dependencies.model.status}</li>
          </ul>
        )}
        {!health && !healthError && (
          <p className="text-slate-500" aria-live="polite">
            checking…
          </p>
        )}
      </section>
    </main>
  );
}
