import { send } from "./teams";

export interface AccountDeletionPreview {
  account_id: string;
  teams: string[];
  blocked_teams: string[];
  sign_in_required: boolean;
  archive_notice: string;
  credential_notice: string;
}

export async function previewAccountDeletion(): Promise<AccountDeletionPreview> {
  return (await send("GET", "/api/v1/me/deletion", "Preview account deletion")).json();
}

export async function deleteAccount(plan: AccountDeletionPreview, confirmation: string): Promise<void> {
  await send("DELETE", "/api/v1/me", "Delete account", {
    confirm_account_id: confirmation,
    confirm_teams: plan.teams,
  });
}

export async function signInAgainForDeletion(): Promise<void> {
  await send("POST", "/api/v1/auth/logout", "Sign out before account deletion");
  window.location.assign(new URL("/login?next=%2Faccount", window.location.origin).href);
}
