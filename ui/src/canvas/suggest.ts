// Quick suggest: one short utility-agent turn proposes child nodes for a
// node. The server answers at once when the agent is quick, else 202 with a
// job to poll. Suggestions land on the proposal (ghost) layer.
import { ContractError, type SuggestResponse } from "../contract";
import type { MapApi, ToastKind } from "../host/adapter";

export const SUGGEST_POLL_MS = 1500;
/** Mirrors the server's SuggestTimeout plus a margin. */
export const SUGGEST_GIVE_UP_MS = 4 * 60_000;

export interface SuggestDeps {
  api: Pick<MapApi, "suggest" | "suggestJob">;
  toast: (message: string, kind?: ToastKind) => void;
  sleep?: (ms: number) => Promise<void>;
  now?: () => number;
}

/** Runs quick suggest on a node and reports the outcome; resolves with the final job state. */
export async function suggestBranches(deps: SuggestDeps, mapId: string, nodeId: string, title: string): Promise<SuggestResponse | null> {
  const sleep = deps.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
  const now = deps.now ?? Date.now;
  let res: SuggestResponse;
  try {
    deps.toast(`Suggesting branches for “${title}”…`, "info");
    res = await deps.api.suggest(mapId, { node_id: nodeId });
    const start = now();
    while (res.status === "running") {
      if (now() - start > SUGGEST_GIVE_UP_MS) {
        deps.toast("Suggestions are taking too long. Check the agents dock later.", "error");
        return res;
      }
      await sleep(SUGGEST_POLL_MS);
      res = await deps.api.suggestJob(mapId, res.job_id);
    }
  } catch (e) {
    deps.toast(e instanceof ContractError ? e.message : "Could not suggest branches.", "error");
    return null;
  }
  deps.toast(suggestMessage(res), res.status === "failed" ? "error" : res.suggestions.length ? "success" : "info");
  return res;
}

export function suggestMessage(res: SuggestResponse): string {
  if (res.status === "failed") return `Suggest failed: ${res.error ?? "the agent gave no answer"}`;
  const n = res.suggestions.length;
  if (!n) return "No new branches to suggest.";
  return `${n} suggested branch${n === 1 ? "" : "es"} to review.`;
}
