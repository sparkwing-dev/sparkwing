import type { ConnectionStatus } from "./api";

export interface ConnectionMessage {
  text: string;
  hint: string;
  signIn?: boolean;
}

export function connectionMessage(
  status: Exclude<ConnectionStatus, "ok">,
  local: boolean,
): ConnectionMessage {
  switch (status) {
    case "unreachable":
      return {
        text: "Cannot reach the sparkwing controller",
        hint: "The API may be down or your network connection was interrupted. Data shown may be stale.",
      };
    case "unauthorized":
      return local
        ? {
            text: "This browser is not signed in",
            hint: "Run `sparkwing dashboard open` and visit the one-time link it prints.",
          }
        : {
            text: "You are not signed in",
            hint: "Your sign-in is missing or has expired.",
            signIn: true,
          };
    case "forbidden":
      return {
        text: "Permission denied",
        hint: "Your account does not have permission for that action.",
      };
    case "session-expired":
      return {
        text: "Your session ended",
        hint: "Sessions expire after a fixed lifetime.",
        signIn: true,
      };
  }
}
