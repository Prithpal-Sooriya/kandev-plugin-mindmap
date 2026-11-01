// Per-run actions in the inspector: stop, message the agent, open its task.
import { ContractError, type Participant, type Run } from "../contract";
import { h, React } from "../runtime";
import type { MapController } from "./controller";

const ACTIVE = new Set(["queued", "running", "waiting", "review"]);

export function canStop(run: Pick<Run, "status">): boolean {
  return ACTIVE.has(run.status);
}

/** A guest run held for the host's approval; only the host sees the button. */
export function canApprove(run: Pick<Run, "status" | "waiting_reason">, me: Pick<Participant, "is_host"> | null | undefined): boolean {
  return run.status === "waiting" && run.waiting_reason === "approval" && !!me?.is_host;
}

/** Runs that wait on a person: a question, an approval or a review. */
export function needsYou(run: Pick<Run, "status">): boolean {
  return run.status === "waiting" || run.status === "review";
}

/** Approves a held run (a coordinator approves all its held agents) and reports it. */
export async function approveRun(ctl: MapController, run: Pick<Run, "id">): Promise<void> {
  try {
    const runs = await ctl.host.api.approveRun(run.id);
    const failed = runs.filter((r) => r.status === "failed");
    if (failed.length) ctl.host.toast(`Approved, but ${failed.length} agent${failed.length > 1 ? "s" : ""} could not start: ${failed[0]!.error ?? "launch failed"}`, "error");
    else ctl.host.toast(runs.length > 1 ? `Approved ${runs.length} agents.` : "Approved. The agent is starting.", "success");
  } catch (e) {
    ctl.host.toast(e instanceof ContractError ? e.message : "Could not approve the run.", "error");
  }
}

/** Coordinator runs (swarms) have no task; messages fan out to their agents.
 * Stopped and failed runs cannot be messaged: continuing means a new run. */
export function canMessage(run: Pick<Run, "status" | "task_id" | "template">): boolean {
  return run.status !== "failed" && run.status !== "stopped" && (!!run.task_id || run.template === "swarm");
}

export function RunActions(props: { ctl: MapController; run: Run }) {
  const { ctl, run } = props;
  const [composing, setComposing] = React.useState(false);
  const [text, setText] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const fail = (what: string) => (e: unknown) => ctl.host.toast(e instanceof ContractError ? e.message : `Could not ${what}.`, "error");

  const stop = async () => {
    setBusy(true);
    await ctl.host.api.stopRun(run.id).then(() => ctl.host.toast("Agent stopped.", "info"), fail("stop the agent"));
    setBusy(false);
  };
  const send = async () => {
    const t = text.trim();
    if (!t) return;
    setBusy(true);
    await ctl.host.api.messageRun(run.id, t).then(() => {
      setText("");
      setComposing(false);
      ctl.host.toast("Message sent.", "success");
    }, fail("send the message"));
    setBusy(false);
  };

  return (
    <div className="mm-runact">
      <div className="mm-row">
        {canApprove(run, ctl.engine.participant) ? (
          <button type="button" className="mm-btn mm-btn-xs mm-btn-primary" disabled={busy} onClick={async () => {
            setBusy(true);
            await approveRun(ctl, run);
            setBusy(false);
          }} data-testid="mm-run-approve">Approve</button>
        ) : null}
        {canStop(run) ? <button type="button" className="mm-btn mm-btn-xs" disabled={busy} onClick={stop} data-testid="mm-run-stop">Stop</button> : null}
        {canMessage(run) ? <button type="button" className={`mm-btn mm-btn-xs${composing ? " mm-btn-on" : ""}`} onClick={() => setComposing(!composing)}>Message</button> : null}
        {run.task_id && ctl.host.kind === "kandev" ? (
          <button type="button" className="mm-btn mm-btn-xs" onClick={() => ctl.host.navigate(`/t/${encodeURIComponent(run.task_id!)}`)}>Open task</button>
        ) : null}
      </div>
      {composing ? (
        <div className="mm-row">
          <input
            className="mm-input mm-grow"
            value={text}
            maxLength={4000}
            placeholder={run.template === "swarm" ? "Message every agent in the swarm" : "Message the agent"}
            ref={(el: HTMLInputElement | null) => el?.focus()}
            onInput={(e: Event) => setText((e.target as HTMLInputElement).value)}
            onKeyDown={(e: KeyboardEvent) => {
              if (e.key === "Enter") send();
              if (e.key === "Escape") setComposing(false);
            }}
            aria-label="Message to the agent"
          />
          <button type="button" className="mm-btn mm-btn-xs mm-btn-primary" disabled={busy || !text.trim()} onClick={send}>Send</button>
        </div>
      ) : null}
    </div>
  );
}
