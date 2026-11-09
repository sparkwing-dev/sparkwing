"use client";

import type { DeviceRunner } from "@/lib/deviceLogin";
import { Panel } from "./TeamShell";

export function DeviceRunnerDetails({
  runner,
  team,
}: {
  runner: DeviceRunner;
  team: string;
}) {
  return (
    <Panel title="Runner">
      <dl className="p-4 text-sm grid grid-cols-[max-content_1fr] gap-x-4 gap-y-2">
        <dt className="text-[var(--muted)]">Kind</dt>
        <dd>
          {runner.personal
            ? `A personal runner on your own account in ${team}.`
            : `Shared with ${team}`}
        </dd>
        <dt className="text-[var(--muted)]">Allowed labels</dt>
        <dd>
          {runner.allowed_labels.length > 0 ? (
            <ul>
              {runner.allowed_labels.map((label, index) => (
                <li key={index} className="font-mono break-all">{label}</li>
              ))}
            </ul>
          ) : (
            "None"
          )}
        </dd>
        {!runner.personal && (
          <>
            <dt className="text-[var(--muted)]">Repositories</dt>
            <dd>
              {runner.repos.length > 0 ? (
                <ul>
                  {runner.repos.map((repo) => (
                    <li key={repo} className="font-mono">
                      {repo}
                    </li>
                  ))}
                </ul>
              ) : (
                "None listed"
              )}
            </dd>
            <dt className="text-[var(--muted)]">Trust</dt>
            <dd>{runner.trust || "Default"}</dd>
            <dt className="text-[var(--muted)]">Reserved pools</dt>
            <dd>
              {runner.reserved_pools.length > 0
                ? runner.reserved_pools.join(", ")
                : "None"}
            </dd>
            <dt className="text-[var(--muted)]">Platform</dt>
            <dd>{runner.platform || "Not set"}</dd>
            <dt className="text-[var(--muted)]">Launcher</dt>
            <dd>{runner.launcher ? "Yes" : "No"}</dd>
          </>
        )}
      </dl>
    </Panel>
  );
}
