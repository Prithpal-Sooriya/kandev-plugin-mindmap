// Share-dialog decisions, kept out of the component so tests can run them
// without a React runtime. The server is the source of truth for the link.
import type { ShareLink } from "../contract";

export interface ShareProblem {
  code: string;
  message: string;
}

export interface ShareDialogModel {
  phase: "idle" | "shared" | "missing" | "error";
  url: string;
  /** brew command to show when the tunnel binary is missing. */
  install: string;
  message: string;
}

const BREW = /brew install [a-z0-9@._+-]+/;

/** Pulls `brew install cloudflared` (or tailscale, ngrok) out of a server message. */
export function installCommand(message: string): string {
  return BREW.exec(message)?.[0] ?? "";
}

/** Header chip: "shared · N guests". */
export function sharedLabel(guests: number): string {
  const n = Number.isFinite(guests) && guests > 0 ? Math.floor(guests) : 0;
  return `shared · ${n} guest${n === 1 ? "" : "s"}`;
}

/** What the Share dialog should show for a link or a contract error. */
export function shareDialogModel(link: ShareLink | null, error: ShareProblem | null): ShareDialogModel {
  if (error?.code === "not_installed") {
    return {
      phase: "missing",
      url: "",
      install: installCommand(error.message) || "brew install cloudflared",
      message: error.message,
    };
  }
  if (error) return { phase: "error", url: "", install: "", message: error.message };
  if (link && (link.status === "active" || link.status === "starting") && link.url) {
    return { phase: "shared", url: link.url, install: "", message: "" };
  }
  if (link?.status === "error") {
    return {
      phase: "error",
      url: "",
      install: link.install_hint ?? "",
      message: link.error ?? "The tunnel failed.",
    };
  }
  return { phase: "idle", url: "", install: "", message: "" };
}
