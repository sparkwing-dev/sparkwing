"use client";

import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import TeamShell, { Panel, errorText } from "@/components/TeamShell";
import { ExtraReposPanel } from "@/components/GitHubAppExtraRepos";
import {
  ConnectGitHubForm,
  InstallationRow,
  SubscriptionForm,
  SubscriptionsTable,
} from "@/components/GitHubAppForms";
import { toast } from "@/components/Toasts";
import { readCSRFCookie } from "@/lib/csrfCookie";
import {
  type GitHubApp,
  type GitHubAppInstallation,
  type GitHubAppRepository,
  type GitHubAppSubscription,
  type SubscriptionDraft,
  connectedAccount,
  deleteSubscription,
  disconnectInstallation,
  draftFromSubscription,
  getGitHubApp,
  githubAppEnabled,
  listInstallationRepositories,
  listSubscriptions,
  putSubscription,
  subscribableRepositories,
} from "@/lib/githubApp";
import { type Capabilities, type Me, canManageTeam } from "@/lib/teams";

export default function GitHubPage() {
  return (
    <TeamShell>
      {(me, caps) => <GitHubSettings me={me} caps={caps} />}
    </TeamShell>
  );
}

function GitHubSettings({ me, caps }: { me: Me; caps: Capabilities | null }) {
  if (!githubAppEnabled(caps)) {
    return (
      <div className="text-sm text-[var(--muted)]">
        This controller has no GitHub App configured.
      </div>
    );
  }
  return <ConnectedGitHub me={me} />;
}

function noSubscribe() {
  return () => {};
}

function emptyToken() {
  return "";
}

function ConnectedGitHub({ me }: { me: Me }) {
  const canManage = canManageTeam(me.active_team.role);
  const csrfToken = useSyncExternalStore(
    noSubscribe,
    readCSRFCookie,
    emptyToken,
  );
  const [app, setApp] = useState<GitHubApp | null>(null);
  const [loadError, setLoadError] = useState("");
  const [repositories, setRepositories] = useState<
    Record<number, GitHubAppRepository[] | undefined>
  >({});
  const [repositoryErrors, setRepositoryErrors] = useState<
    Record<number, string>
  >({});
  const [subscriptions, setSubscriptions] = useState<
    GitHubAppSubscription[] | null
  >(null);
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<SubscriptionDraft | null>(null);

  const load = useCallback(async () => {
    try {
      const [nextApp, nextSubs] = await Promise.all([
        getGitHubApp(),
        listSubscriptions(),
      ]);
      setApp(nextApp);
      setSubscriptions(nextSubs);
      setLoadError("");
      for (const inst of nextApp.installations) {
        listInstallationRepositories(inst.installation_id).then(
          (repos) =>
            setRepositories((prev) => ({
              ...prev,
              [inst.installation_id]: repos,
            })),
          (err) =>
            setRepositoryErrors((prev) => ({
              ...prev,
              [inst.installation_id]: errorText(err),
            })),
        );
      }
    } catch (err) {
      setLoadError(errorText(err));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  useEffect(() => {
    const account = connectedAccount(window.location.search);
    if (!account) return;
    toast(`Connected ${account} to this team`, "success");
    window.history.replaceState(null, "", window.location.pathname);
  }, []);

  async function disconnect(inst: GitHubAppInstallation) {
    if (
      !window.confirm(
        `Disconnect ${inst.account_login}? Its repositories stop starting runs here, and their subscriptions are removed. The App stays installed on GitHub.`,
      )
    ) {
      return;
    }
    setBusy(true);
    try {
      await disconnectInstallation(inst.installation_id);
      toast(`Disconnected ${inst.account_login}`, "success");
      await load();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(false);
    }
  }

  async function save(draft: SubscriptionDraft) {
    setBusy(true);
    try {
      const saved = await putSubscription(draft);
      toast(`${saved.repository} runs ${saved.pipeline}`, "success");
      setEditing(null);
      await load();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(false);
    }
  }

  async function remove(sub: GitHubAppSubscription) {
    if (
      !window.confirm(`Stop running ${sub.pipeline} for ${sub.repository}?`)
    ) {
      return;
    }
    setBusy(true);
    try {
      await deleteSubscription(sub.repository_id, sub.pipeline);
      toast(`Removed ${sub.pipeline} from ${sub.repository}`, "success");
      await load();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(false);
    }
  }

  if (loadError) {
    return <div className="text-sm text-red-300">{loadError}</div>;
  }
  if (app === null || subscriptions === null) {
    return <div className="text-xs text-[var(--muted)]">Loading…</div>;
  }
  const installations = app.installations;
  const choices = subscribableRepositories(installations, repositories);

  return (
    <>
      <Panel
        title="Connected accounts"
        hint="An installation of the GitHub App proves this team controls those repositories. Only an owner who administers the GitHub account connects it."
        action={
          canManage ? (
            <ConnectGitHubForm
              csrfToken={csrfToken}
              connected={installations.length > 0}
            />
          ) : null
        }
      >
        {installations.length === 0 ? (
          <div className="p-4 text-xs text-[var(--muted)]">
            {canManage
              ? "No GitHub account connected. Connect GitHub to run pipelines on pushes and pull requests."
              : "No GitHub account connected. A team owner connects one."}
          </div>
        ) : (
          <ul>
            {installations.map((inst) => (
              <InstallationRow
                key={inst.installation_id}
                inst={inst}
                repositories={repositories[inst.installation_id]}
                repositoriesError={repositoryErrors[inst.installation_id]}
                canManage={canManage}
                busy={busy}
                onDisconnect={disconnect}
              />
            ))}
          </ul>
        )}
      </Panel>
      <Panel
        title="Runs from GitHub"
        hint="Each subscription runs one pipeline on matching pushes or pull requests. Set push branches for deploy pipelines. Pull requests from forks aren't run."
      >
        <SubscriptionsTable
          subscriptions={subscriptions}
          canManage={canManage}
          busy={busy}
          onEdit={(sub) => setEditing(draftFromSubscription(sub))}
          onRemove={remove}
        />
      </Panel>
      {canManage && installations.length > 0 ? (
        <Panel
          title={editing ? "Edit subscription" : "Add a subscription"}
          hint="Saving a repository and pipeline that already exist replaces their events."
        >
          <SubscriptionForm
            key={editing ? `${editing.repository}/${editing.pipeline}` : "new"}
            repositories={choices}
            initial={editing ?? undefined}
            busy={busy}
            onSave={save}
            onCancel={editing ? () => setEditing(null) : undefined}
          />
        </Panel>
      ) : null}
      {installations.length > 0 ? (
        <ExtraReposPanel repositories={choices} canManage={canManage} />
      ) : null}
      {!canManage ? (
        <div className="text-xs text-[var(--muted)]">
          Team owners connect accounts and change subscriptions.
        </div>
      ) : null}
    </>
  );
}
