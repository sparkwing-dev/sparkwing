"use client";

import { useRef, useState } from "react";
import { Panel, errorText, quietButtonClass } from "./TeamShell";
import { type AutomationPreview, type RunnerBindings, previewAutomation, enableAutomation, disableAutomation, getRunnerBindings, enableGitHubRunner, disableGitHubRunner } from "@/lib/githubApp";
import { toast } from "./Toasts";

export default function GitHubOnboarding({ repositories, canManage, teamSlug }: { repositories: string[]; canManage: boolean; teamSlug: string }) {
  const [repository, setRepository] = useState("");
  const [preview, setPreview] = useState<AutomationPreview | null>(null);
  const [runners, setRunners] = useState<RunnerBindings | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const generation = useRef(0);
  const controlClass = `${quietButtonClass} min-h-11 focus-visible:ring-2 focus-visible:ring-[var(--accent)]`;
  const bound = runners?.bindings.find((binding) => binding.repository_id === preview?.repository_id);

  async function discover(selected: string) {
    const request = ++generation.current;
    setRepository(selected);
    setPreview(null);
    setRunners(null);
    setError("");
    if (!selected) { setBusy(false); return; }
    setBusy(true);
    try {
      const [nextPreview, nextRunners] = await Promise.all([previewAutomation(selected), getRunnerBindings()]);
      if (request !== generation.current) return;
      setPreview(nextPreview);
      setRunners(nextRunners);
    } catch (err) {
      if (request === generation.current) setError(errorText(err));
    } finally {
      if (request === generation.current) setBusy(false);
    }
  }

  async function change(action: () => Promise<unknown>) {
    setBusy(true);
    setError("");
    try {
      await action();
      toast("Repository settings saved", "success");
      await discover(repository);
    } catch (err) {
      setError(errorText(err));
      toast(errorText(err), "error");
      setBusy(false);
    }
  }

  async function copyWorkflow() {
    try {
      await navigator.clipboard.writeText(runners?.workflow ?? "");
      toast("Workflow copied", "success");
    } catch (err) { setError(errorText(err)); }
  }

  return <Panel title="Set up repository runs">
    <div className="space-y-4 p-4 text-sm">
      <label className="block">Repository to set up
        <select className="mt-2 block min-h-11 w-full rounded-[var(--radius-control)] border border-[var(--border)] bg-[var(--surface)] p-2 focus-visible:ring-2 focus-visible:ring-[var(--accent)]" value={repository} disabled={busy} onChange={(event) => discover(event.target.value)}>
          <option value="">Choose a connected repository</option>
          {repositories.map((name) => <option key={name}>{name}</option>)}
        </select>
      </label>
      {busy && <p role="status">Loading repository setup…</p>}
      {error && <p role="alert" className="text-[var(--danger)]">{error}</p>}
      {repository && !busy && <button className={controlClass} onClick={() => discover(repository)}>Refresh repository setup</button>}
      {preview && <>
        <h3 className="font-medium">Triggers from repository configuration</h3>
        <p>Read .sparkwing/sparkwing.yaml from {preview.default_branch || "the default branch"}. Future configuration changes control these triggers.</p>
        {preview.source_sha && <p className="break-all text-xs text-[var(--muted)]">Source commit: {preview.source_sha}</p>}
        {preview.status !== "ready" && <p role="status" className="text-[var(--warning)]">{preview.error || `Configuration ${preview.status}`}</p>}
        {preview.status === "ready" && preview.pipelines.length === 0 && <p>No push or pull request declarations found.</p>}
        <ul className="space-y-2">
          {preview.pipelines.map((pipeline) => <li key={pipeline.pipeline} className="break-words rounded-[var(--radius-control)] border border-[var(--border)] p-3">
            <span className="font-medium">{pipeline.pipeline}</span>
            {pipeline.push && <p>Push branches: {pipeline.branches.length ? pipeline.branches.join(", ") : "all branches"}</p>}
            {pipeline.pull_request && <p>Pull requests: {pipeline.actions.join(", ")}; base branches: {pipeline.base_branches.length ? pipeline.base_branches.join(", ") : "all branches"}</p>}
            {pipeline.manual_override && <p className="text-[var(--warning)]">Manual subscription controls this pipeline. Remove it below to use repository declarations.</p>}
          </li>)}
        </ul>
        <p className="text-xs text-[var(--muted)]">Branch patterns use Go path.Match: * does not cross /. Fork pull requests do not run. Invalid or unsupported configuration stops declaration-based dispatch.</p>
        <p role="status">Repository automation: {preview.enabled ? "enabled" : "not enabled"}</p>
        {canManage && <button className={controlClass} disabled={busy || (!preview.enabled && (preview.status !== "ready" || preview.pipelines.length === 0))} onClick={() => change(() => preview.enabled ? disableAutomation(preview.repository_id) : enableAutomation(repository))}>
          {preview.enabled ? "Disable repository automation" : "Enable repository declarations"}
        </button>}
        <h3 className="font-medium">Execution on GitHub Actions</h3>
        <p>Allow this repository&apos;s GitHub Actions jobs to execute its work for team <code>{teamSlug}</code> using its Actions minutes. Team machines remain eligible for work.</p>
        <p role="status">GitHub Actions permission: {bound ? "granted" : "not granted"}. Workflow installation is not verified here.</p>
        {canManage && <button className={controlClass} disabled={busy} onClick={() => change(() => bound ? disableGitHubRunner(bound.repository_id) : enableGitHubRunner(repository))}>
          {bound ? "Revoke GitHub Actions permission" : "Allow GitHub Actions execution"}
        </button>}
        {bound && runners?.workflow && <>
          <p>Commit this workflow as <code>.github/workflows/sparkwing.yaml</code>. It starts a worker on branch pushes; GitHub Actions workers cannot execute pull request or tag runs.</p>
          <div className="flex flex-wrap gap-3">
            <button className={controlClass} onClick={copyWorkflow}>Copy workflow</button>
            <a className={`${controlClass} inline-flex items-center`} download="sparkwing.yaml" href={`data:text/yaml;charset=utf-8,${encodeURIComponent(runners.workflow)}`}>Download workflow</a>
          </div>
          <details><summary className="min-h-11 cursor-pointer py-3 focus-visible:ring-2 focus-visible:ring-[var(--accent)]">View generated workflow</summary><pre className="mt-2 overflow-x-auto text-xs">{runners.workflow}</pre></details>
        </>}
      </>}
      {!canManage && <p>A team owner can enable repository declarations and GitHub Actions execution.</p>}
    </div>
  </Panel>;
}
