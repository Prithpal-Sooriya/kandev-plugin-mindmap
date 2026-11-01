// Per-run actions in the inspector: stop, message the agent, open its task.
import { ContractError, type Run } from "../contract";
import { h, React } from "../runtime";
import type { MapController } from "./controller";

const ACTIVE = new Set(["queued", "running", "waiting", "review"]);

export function canStop(run: Pick<Run, "status">): boolean {
  return ACTIVE.has(run.status);
}

/** Coordinator runs (swarms) have no task; messages fan out to their agents. */
export function canMessage(run: Pick<Run, "status" | "task_id" | "template">): boolean {
  return run.status !== "failed" && (!!run.task_id || run.template === "swarm");
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
