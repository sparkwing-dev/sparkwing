// The deployment's support contact comes from the controller's runtime config
// (--support-contact); a deployment without one, such as a self-hosted
// controller, sends people to their Sparkwing administrator instead.

export const DOCS_URL = "https://sparkwing.dev/docs/";

export type SupportContact = { label: string; href: string };

type RuntimeWindow = { __SPARKWING_SUPPORT_CONTACT__?: string };

// supportContact turns the configured value into a link: an email address
// becomes a mailto link and an http(s) URL is used as is. Anything else is
// treated as unset rather than rendered as a broken link.
export function supportContact(raw: string | undefined): SupportContact | null {
  const value = (raw ?? "").trim();
  if (value === "") return null;
  if (/^https?:\/\/\S+$/i.test(value)) return { label: value, href: value };
  if (/^[^\s@/:]+@[^\s@/:]+\.[^\s@/:]+$/.test(value)) {
    return { label: value, href: `mailto:${value}` };
  }
  return null;
}

export function readSupportContact(): SupportContact | null {
  if (typeof window === "undefined") return null;
  return supportContact(
    (window as unknown as RuntimeWindow).__SPARKWING_SUPPORT_CONTACT__,
  );
}

// limitHelp is the sentence the console appends where a limit refused work.
export function limitHelp(contact: SupportContact | null): string {
  return contact
    ? `Contact ${contact.label} to raise this limit.`
    : "Contact your Sparkwing administrator to raise this limit.";
}
