"use client";

import Link from "next/link";
import { useRef, useState } from "react";
import { buttonClass, errorText, inputClass, quietButtonClass } from "./TeamShell";
import CopyField from "./CopyField";
import CLISignIn from "./CLISignIn";
import { type AppRunner, type AutomationPreview, type RunnerBindings, appRunnerChoices, previewAutomation, enableAutomation, disableAutomation, getRunnerBindings, enableGitHubRunner, disableGitHubRunner } from "@/lib/githubApp";
import { toast } from "./Toasts";

export default function GitHubOnboarding({ repositories, canManage, teamSlug }: { repositories: string[]; canManage: boolean; teamSlug: string }) {
  const [repository, setRepository] = useState("");
  const [preview, setPreview] = useState<AutomationPreview | null>(null);
  const [runner, setRunner] = useState<AppRunner>("github-actions");
  const [runners, setRunners] = useState<RunnerBindings | null>(null);
  const [workflowConfirmed, setWorkflowConfirmed] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const generation = useRef(0);
  const bound = runners?.bindings.find((binding) => binding.repository_id === preview?.repository_id);
  const triggersFound = preview?.status === "ready" && preview.pipelines.some((pipeline) => pipeline.push);
  const actionsSelected = preview?.runner === "github-actions";
  const permissionsReady = preview?.enabled && bound && actionsSelected;
  const manualOverride = preview?.pipelines.some((pipeline) => pipeline.manual_override);
  const ready = triggersFound && permissionsReady && workflowConfirmed && !manualOverride;
  const controlClass = `${quietButtonClass} min-h-11`;

  async function discover(selected: string) {
    const request = ++generation.current;
    if (selected !== repository) {
      setWorkflowConfirmed(false);
    }
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
      setRunner(nextPreview.enabled ? nextPreview.runner ?? "anywhere" : "github-actions");
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
      await discover(repository);
      toast("Repository settings saved", "success");
    } catch (err) {
      // A combined action can save one permission before the other fails.
      await discover(repository);
      setError(`Setup could not finish. ${errorText(err)} Saved permissions are shown below. Retry to finish the remaining step.`);
      setBusy(false);
    }
  }

  async function enableActions() {
    if (!bound) await enableGitHubRunner(repository);
    if (!preview?.enabled || !actionsSelected) await enableAutomation(repository, "github-actions");
  }

  return <section aria-label="Repository setup" className="space-y-6">
    <ol aria-label="Setup progress" className="grid grid-cols-2 gap-3 rounded-lg bg-[var(--surface)] p-4 text-xs sm:grid-cols-4">
      {[['GitHub connected', true], ['Push triggers found', triggersFound], ['Runs enabled', permissionsReady], ['GitHub setup confirmed', ready]].map(([label, complete], index) =>
        <li key={String(label)} className={complete ? "text-emerald-300" : "text-[var(--muted)]"}>
          <span aria-hidden="true" className="mr-2">{complete ? "✓" : `${index + 1}.`}</span>{label}<span className="sr-only">: {complete ? "complete" : "pending"}</span>
        </li>)}
    </ol>
    <div className="space-y-2">
      <div className="flex flex-wrap items-end gap-2">
      <label className="block min-w-48 flex-1 text-sm">Repository to set up
        <select className={`${inputClass} mt-2 block min-h-11 w-full`} value={repository} disabled={busy} onChange={(event) => discover(event.target.value)}>
          <option value="">Choose a connected repository</option>
          {repositories.map((name) => <option key={name}>{name}</option>)}
        </select>
      </label>
      {repository && !busy && <button aria-label="Refresh repository setup" className={controlClass} onClick={() => discover(repository)}>Refresh</button>}
      </div>
      <p className="text-sm text-[var(--muted)]">GitHub chooses which code Sparkwing can read. Here you choose which repository starts runs.</p>
      {repositories.length === 0 && <p className="text-sm text-amber-200">No repositories available. Choose repositories in the connected account&apos;s GitHub settings, then refresh this page.</p>}
    </div>
    {busy && <p role="status" className="text-sm">Checking repository setup…</p>}
    {error && <p role="alert" className="text-sm text-red-300">{error}</p>}
    {preview && <>
      <div className="grid gap-6 md:grid-cols-2">
      <div className="space-y-3 border-t border-[var(--border)] pt-5 text-sm">
        <h3 className="text-base font-semibold">What will run</h3>
        <p className="text-[var(--muted)]">From <code>.sparkwing/sparkwing.yaml</code> on {preview.default_branch || "the default branch"}. Edit that file to change when pipelines run.</p>
        {preview.status !== "ready" && <p role="status" className="text-amber-200">{preview.error || `Configuration ${preview.status}`}. Commit a valid pipeline configuration to {preview.default_branch || "the default branch"}, then refresh repository setup.</p>}
        {preview.status === "ready" && !triggersFound && <p className="text-amber-200">No push triggers found. Add <code>on.push</code> to your pipeline configuration for GitHub Actions. Pull request runs need Cloud or a team runner.</p>}
        <ul className="space-y-3">
          {preview.pipelines.map((pipeline) => <li key={pipeline.pipeline} className="break-words">
            <span className="font-semibold">{pipeline.pipeline}</span>
            {pipeline.push && <p className="text-[var(--muted)]">Pushes to {pipeline.branches.length ? pipeline.branches.join(", ") : "any branch"}</p>}
            {pipeline.pull_request && <p className="text-[var(--muted)]">Pull requests into {pipeline.base_branches.length ? pipeline.base_branches.join(", ") : "any branch"} (requires Cloud or a team runner)</p>}
            {pipeline.manual_override && <p className="text-amber-200">A manual subscription overrides these triggers. Remove it in Advanced below to use this setup.</p>}
          </li>)}
        </ul>
        {preview.skipped.map((skip) => <p key={skip.pipeline} className="text-amber-200">Skipped {skip.pipeline}: {skip.error}</p>)}
      </div>
      <div className="space-y-3 border-t border-[var(--border)] pt-5 text-sm">
        <h3 className="text-base font-semibold">Run on GitHub Actions</h3>
        <p className="text-[var(--muted)]">This turns on automatic runs and lets this repository&apos;s Actions jobs execute work for <code>{teamSlug}</code>, using your GitHub Actions minutes.</p>
        {preview.pipelines.some((pipeline) => pipeline.pull_request) && <p className="text-amber-200">GitHub Actions handles branch pushes only. Choose another run location below for pull request runs.</p>}
        <ul aria-label="Saved permissions" className="space-y-1 text-[var(--muted)]">
          <li>Automatic runs: {preview.enabled ? "on" : "off"}</li>
          <li>GitHub Actions access: {bound ? "allowed" : "not allowed"}</li>
          <li>Run location: {appRunnerChoices.find((choice) => choice.value === preview.runner)?.label || preview.runner || "not selected"}</li>
        </ul>
        {canManage && !permissionsReady && <button className={buttonClass} disabled={busy || !triggersFound} onClick={() => change(enableActions)}>Enable runs on GitHub Actions</button>}
        {!canManage && <p className="text-amber-200">Ask a team owner to enable runs and allow GitHub Actions access.</p>}
        <details>
          <summary className="min-h-11 cursor-pointer py-3 text-[var(--muted)]">Other run locations and separate permissions</summary>
          <div className="space-y-3 pt-2">
            <label className="block">Runs on
              <select className={`${inputClass} ml-2 min-h-11 max-w-full`} value={runner} disabled={busy || !canManage} onChange={(event) => setRunner(event.target.value as AppRunner)}>
                {appRunnerChoices.map((choice) => <option key={choice.value} value={choice.value}>{choice.label}</option>)}
                {runner.startsWith("pool=") && <option value={runner}>{runner}</option>}
              </select>
            </label>
            {canManage && <div className="flex flex-wrap gap-2">
              <button className={controlClass} disabled={busy || preview.status !== "ready" || !preview.pipelines.length} onClick={() => change(() => enableAutomation(repository, runner))}>{preview.enabled ? "Save run location" : "Enable automatic runs only"}</button>
              {preview.enabled && <button className={controlClass} disabled={busy} onClick={() => change(() => disableAutomation(preview.repository_id))}>Disable automatic runs</button>}
              <button className={controlClass} disabled={busy} onClick={() => change(() => bound ? disableGitHubRunner(bound.repository_id) : enableGitHubRunner(repository))}>{bound ? "Revoke GitHub Actions permission" : "Allow GitHub Actions execution only"}</button>
            </div>}
            <p className="text-[var(--muted)]">Branch patterns use Go path.Match: * does not cross /. Fork pull requests do not run. GitHub Actions workers handle branch pushes, not pull requests or tags. Team machines remain eligible for work.</p>
          </div>
        </details>
      </div>
      </div>
      {bound && <div className="space-y-4 border-t border-[var(--border)] pt-5 text-sm">
        <h3 className="text-base font-semibold">Workflow and team variable</h3>
        <p className="text-[var(--muted)]">Already have a working Sparkwing workflow? Keep it. Check the team setting and workflow below. Sparkwing cannot inspect your workflow or GitHub variables here.</p>
        <div className="space-y-2">
          <a className="inline-flex min-h-11 items-center text-indigo-300 underline" href={`https://github.com/${repository}/settings/variables/actions`} target="_blank" rel="noreferrer">Open repository variables on GitHub ↗</a>
          <p>If your workflow uses <code>vars.SPARKWING_TEAM</code>, create that repository variable with this exact value. The starter workflow already includes it in <code>--team</code>.</p>
          <CopyField value={teamSlug} label="SPARKWING_TEAM value" />
        </div>
        <div className="space-y-2">
          <a className="inline-flex min-h-11 items-center text-indigo-300 underline" href={`https://github.com/${repository}/actions`} target="_blank" rel="noreferrer">Open repository workflows on GitHub ↗</a>
          {runners?.workflow && <details>
            <summary className="min-h-11 cursor-pointer py-3 text-[var(--muted)]">Need a workflow? Copy this starter</summary>
            <p className="mb-3">Commit as <code>.github/workflows/sparkwing.yaml</code>. Keep your existing workflow if it already starts a Sparkwing runner.</p>
            <CopyField value={runners.workflow} label="Starter workflow" multiline />
            <a className="mt-2 inline-flex min-h-11 items-center text-indigo-300 underline" download="sparkwing.yaml" href={`data:text/yaml;charset=utf-8,${encodeURIComponent(runners.workflow)}`}>Download workflow</a>
          </details>}
        </div>
        <label className="flex min-h-11 items-center gap-3"><input type="checkbox" checked={workflowConfirmed} onChange={(event) => setWorkflowConfirmed(event.target.checked)} />My enabled workflow starts a Sparkwing runner on pushes for team {teamSlug}</label>
      </div>}
      {ready && <div role="status" className="rounded-lg bg-emerald-500/10 p-5 text-sm">
        <h3 className="text-lg font-semibold text-emerald-300">Ready for your next push</h3>
        <p className="mt-2">{preview.pipelines.filter((pipeline) => pipeline.push).map((pipeline) => `${pipeline.pipeline} on pushes to ${pipeline.branches.length ? pipeline.branches.join(", ") : "any branch"}`).join("; ")} will run on GitHub Actions.</p>
        <p className="mt-2 text-[var(--muted)]">GitHub settings confirmed by you for this visit. Push a commit to verify the first run end to end.</p>
        <Link className="mt-3 inline-flex min-h-11 items-center text-indigo-300 underline" href="/runs">Follow your first run →</Link>
      </div>}
      <details className="border-t border-[var(--border)] pt-2">
        <summary className="min-h-11 cursor-pointer py-3 text-sm">Optional: use the CLI or connect your own machine</summary>
        <CLISignIn teamSlug={teamSlug} />
        <Link href="/team/machines" className="inline-flex min-h-11 items-center text-sm text-indigo-300 underline">CLI access and machines →</Link>
      </details>
    </>}
  </section>;
}
