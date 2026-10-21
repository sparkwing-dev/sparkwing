"use client";

import { useCallback, useEffect, useState } from "react";
import { Panel, buttonClass, errorText } from "./TeamShell";
import Tooltip from "./Tooltip";
import { toast } from "./Toasts";
import { fmtAgo, fmtFullDate } from "@/lib/timeFormat";
import {
  type FailedGitHubDelivery,
  listFailedGitHubDeliveries,
  processGitHubDeliveryAgain,
} from "@/lib/githubApp";

export default function FailedGitHubDeliveries() {
  const [deliveries, setDeliveries] = useState<FailedGitHubDelivery[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(async () => {
    try {
      setDeliveries(await listFailedGitHubDeliveries());
      setError("");
    } catch (err) {
      setError(errorText(err));
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  async function processAgain(id: string) {
    setBusy(true);
    try {
      await processGitHubDeliveryAgain(id);
      toast("Delivery queued for processing", "success");
      await load();
    } catch (err) {
      setError(errorText(err));
      toast(errorText(err), "error");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Panel title="Pushes we couldn’t process">
      <p className="text-sm text-[var(--muted)]">
        Failed deliveries stay here until you process them again or their retention
        period ends. Processing does not retry automatically.
      </p>
      {error && <p role="alert">{error}</p>}
      {deliveries === null ? (
        !error && <p className="text-sm">Loading failed deliveries...</p>
      ) : deliveries.length === 0 ? (
        <p className="text-sm">No failed deliveries.</p>
      ) : (
        <ul className="space-y-3">
          {deliveries.map((delivery) => (
            <li key={delivery.id} className="flex items-center justify-between gap-4">
              <div>
                <p>
                  {delivery.event} · {" "}
                  <Tooltip content={fmtFullDate(delivery.received_at)}>
                    <time dateTime={delivery.received_at}>
                      {fmtAgo(delivery.received_at)}
                    </time>
                  </Tooltip>
                </p>
                <p className="text-sm">{delivery.last_error}</p>
                <p className="text-xs text-[var(--muted)]">{delivery.id}</p>
              </div>
              <button
                className={buttonClass}
                disabled={busy}
                onClick={() => void processAgain(delivery.id)}
              >
                Process again
              </button>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  );
}
