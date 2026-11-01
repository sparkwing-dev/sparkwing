"use client";

import { useCallback, useEffect, useState } from "react";
import { Notice, Panel, buttonClass, errorText, inputClass } from "./TeamShell";
import { toast } from "./Toasts";
import {
  getWarmCapacity, setWarmCapacity,
  type WarmCapacity, type WarmCapacityClass, type WarmCapacitySettings,
} from "@/lib/operator";

const seconds = (n: number | null) => n === null ? "—" : `${n.toFixed(1)}s`;

export function WarmCapacityView() {
  const [data, setData] = useState<WarmCapacity | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(() => getWarmCapacity().then(
    (next) => { setData(next); setError(""); },
    (err) => setError(errorText(err)),
  ), []);
  useEffect(() => { void load(); }, [load]);
  if (!data) return <Notice>{error || "Loading…"}</Notice>;
  return <div className="space-y-4">
    <h2 className="text-lg font-bold">Warm capacity</h2>
    <p className="text-sm text-[var(--muted)]">Off is the default. Manual keeps a fixed number of nodes warm. Auto adjusts within your bounds using cold starts and the start-time target.</p>
    {error ? <p role="alert">{error}</p> : null}
    <table className="w-full text-sm">
      <caption className="sr-only">Account and team activity</caption>
      <thead><tr><th className="text-left">Activity</th><th>24h</th><th>7d</th></tr></thead>
      <tbody>
        <tr><th className="text-left">New signups</th><td className="text-center">{data.new_signups_24h}</td><td className="text-center">{data.new_signups_7d}</td></tr>
        <tr><th className="text-left">Active teams</th><td className="text-center">{data.active_teams_24h}</td><td className="text-center">{data.active_teams_7d}</td></tr>
      </tbody>
    </table>
    {data.classes.map((item) => <WarmClass key={JSON.stringify(item)} item={item} onSaved={load} />)}
  </div>;
}

function WarmClass({ item, onSaved }: { item: WarmCapacityClass; onSaved: () => Promise<void> }) {
  const [settings, setSettings] = useState<WarmCapacitySettings>({
    class: item.class, mode: item.mode, manual_count: item.manual_count,
    auto_min: item.auto_min, auto_max: item.auto_max,
    start_time_target_seconds: item.start_time_target_seconds,
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const count = (label: string, field: "manual_count" | "auto_min" | "auto_max" | "start_time_target_seconds") => <label className="block">
    <span className="block text-sm mb-1">{label}</span>
    <input className={inputClass} type="number" required min={0} step={field === "start_time_target_seconds" ? "any" : "1"} value={Number.isNaN(settings[field]) ? "" : settings[field]} onChange={(e) => setSettings({ ...settings, [field]: e.target.valueAsNumber })} />
  </label>;
  return <Panel title={item.class} hint={`Warm nodes: ${item.warm} · Desired nodes: ${item.desired}`}>
    <div className="p-4 space-y-4">
      <table className="w-full text-sm">
        <caption className="sr-only">{item.class} start and capacity metrics</caption>
        <thead><tr><th className="text-left">Job start time</th><th>1h</th><th>24h</th></tr></thead>
        <tbody>
          <tr><th className="text-left">p50 start</th><td className="text-center">{seconds(item.start_1h.p50_seconds)}</td><td className="text-center">{seconds(item.start_24h.p50_seconds)}</td></tr>
          <tr><th className="text-left">p95 start</th><td className="text-center">{seconds(item.start_1h.p95_seconds)}</td><td className="text-center">{seconds(item.start_24h.p95_seconds)}</td></tr>
        </tbody>
      </table>
      <p className="text-sm">Cold starts (24h / 7d): {item.cold_starts_24h} / {item.cold_starts_7d}. Upsizes (24h / 7d): {item.upsizes_24h} / {item.upsizes_7d}.</p>
      <form aria-label={`${item.class} warm capacity`} className="space-y-3" onSubmit={async (event) => {
        event.preventDefault();
        if (settings.start_time_target_seconds <= 0) { setError("Start-time target must be greater than zero."); return; }
        if (settings.auto_min > settings.auto_max) { setError("Auto maximum must be at least the minimum."); return; }
        setBusy(true); setError("");
        try {
          await setWarmCapacity(settings);
          toast(`Saved warm capacity for ${item.class}.`);
          await onSaved();
        } catch (err) { setError(errorText(err)); }
        finally { setBusy(false); }
      }}>
        <fieldset disabled={busy} className="flex flex-wrap items-end gap-3">
          <label><span className="block text-sm mb-1">Mode</span><select className={inputClass} value={settings.mode} onChange={(e) => setSettings({ ...settings, mode: e.target.value as WarmCapacitySettings["mode"] })}>
            <option value="off">Off</option><option value="manual">Manual</option><option value="auto">Auto</option>
          </select></label>
          {count("Manual count", "manual_count")}
          {count("Auto minimum", "auto_min")}
          {count("Auto maximum", "auto_max")}
          {count("Start-time target (seconds)", "start_time_target_seconds")}
          <button className={buttonClass} type="submit">{busy ? "Saving…" : "Save"}</button>
        </fieldset>
        {error ? <p role="alert" className="text-sm text-red-400">{error}</p> : null}
      </form>
    </div>
  </Panel>;
}
