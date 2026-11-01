// Agents dock: a drawer with every run of the map (as a tree: coordinators
// over their agents), the activity feed and the participants. The header
// "needs you" chip opens it filtered to runs waiting on a person.
import { Fragment, h, React } from "../runtime";
import type { MapController } from "./controller";
import type { Run } from "../contract";
import { Icon, ICONS } from "./icons";
import { HarnessBadge } from "./agent";
import { approveRun, canApprove, canStop, needsYou } from "./RunActions";

export type DockFilter = "all" | "needs";

/** The runs the dock lists for a filter. A child that needs you keeps its parent visible. */
export function dockRuns(runs: Run[], filter: DockFilter): Run[] {
  if (filter === "all") return runs;
  const byId = new Map(runs.map((r) => [r.id, r]));
  const keep = new Set<string>();
  for (const r of runs) {
    if (!needsYou(r)) continue;
    for (let cur: Run | undefined = r; cur && !keep.has(cur.id); cur = cur.parent_run_id ? byId.get(cur.parent_run_id) : undefined) keep.add(cur.id);
  }
  return runs.filter((r) => keep.has(r.id));
}

export function AgentsDock(props: { ctl: MapController; onReveal: (nodeId: string) => void; onClose: () => void }) {
  const { ctl } = props;
  const [tab, setTab] = React.useState("runs" as "runs" | "activity" | "participants");
  return (
    <aside className="mm-drawer" aria-label="Agents dock" data-testid="mm-dock">
      <header className="mm-insp-head">
        <strong>Agents</strong>
        <span className="mm-spacer" />
        <button type="button" className="mm-icon-btn" onClick={props.onClose} aria-label="Close agents dock"><Icon d={ICONS.close} /></button>
      </header>
      <div className="mm-drawer-tabs" role="tablist">
        {(["runs", "activity", "participants"] as const).map((t) => (
          <button key={t} type="button" role="tab" aria-selected={tab === t} className={tab === t ? "mm-on" : ""} onClick={() => setTab(t)}>
            {t === "runs" ? "Runs" : t === "activity" ? "Activity" : "People"}
          </button>
        ))}
      </div>
      <div className="mm-drawer-body mm-scroll">
        {tab === "runs" ? <RunsList ctl={ctl} onReveal={props.onReveal} /> : tab === "activity" ? <ActivityFeed ctl={ctl} /> : <ParticipantsList ctl={ctl} />}
      </div>
    </aside>
  );
}

function RunsList(props: { ctl: MapController; onReveal: (nodeId: string) => void }) {
  const { ctl } = props;
  const all = [...ctl.engine.runs.values()].sort((a, b) => b.created_at.localeCompare(a.created_at));
  const waiting = all.filter(needsYou).length;
  const filter: DockFilter = ctl.dockFilter === "needs" && waiting ? "needs" : "all";
  const runs = dockRuns(all, filter);
  const setFilter = (f: DockFilter) => {
    ctl.dockFilter = f;
    ctl.bump();
  };
  const ids = new Set(runs.map((r) => r.id));
  const kids = new Map<string, Run[]>();
  for (const r of runs) {
    if (r.parent_run_id && ids.has(r.parent_run_id)) kids.set(r.parent_run_id, [...(kids.get(r.parent_run_id) ?? []), r]);
  }
  const roots = runs.filter((r) => !r.parent_run_id || !ids.has(r.parent_run_id));
  const render = (run: Run, depth: number): unknown => (
    <div key={run.id} className="mm-drawer-run-group">
      <RunRow ctl={ctl} run={run} depth={depth} onReveal={props.onReveal} />
      {(kids.get(run.id) ?? []).slice().reverse().map((c) => render(c, depth + 1))}
    </div>
  );
  return (
    <div className="mm-drawer-runs">
      {waiting ? (
        <div className="mm-row mm-drawer-filter" role="radiogroup" aria-label="Filter runs">
          <button type="button" className={`mm-btn mm-btn-xs${filter === "needs" ? " mm-btn-on" : ""}`} onClick={() => setFilter("needs")} data-testid="mm-dock-needs">
            Needs you ({waiting})
          </button>
          <button type="button" className={`mm-btn mm-btn-xs${filter === "all" ? " mm-btn-on" : ""}`} onClick={() => setFilter("all")}>All ({all.length})</button>
        </div>
      ) : null}
      {roots.length ? roots.map((r) => render(r, 0)) : <p className="mm-muted mm-pad">No agents yet.</p>}
    </div>
  );
}

function RunRow(props: { ctl: MapController; run: Run; depth: number; onReveal: (nodeId: string) => void }) {
  const { ctl, run, depth } = props;
  const participant = ctl.engine.participants.get(run.participant_id);
  const node = ctl.view.nodes.get(run.node_id);
  const [busy, setBusy] = React.useState(false);
  const elapsed = Math.max(0, ((Date.parse(run.ended_at ?? "") || Date.now()) - Date.parse(run.created_at)) / 60000);
  const status = `${run.status}${run.waiting_reason ? ` (${run.waiting_reason})` : ""}`;
  return (
    <div className={`mm-drawer-run mm-depth-${Math.min(depth, 3)}`} data-testid="mm-dock-run">
      <div className="mm-row">
        <HarnessBadge harness={run.harness} color={participant?.color} title={participant?.name} />
        <strong className="mm-ellipsis mm-grow">{run.profile_name ?? run.template}{run.lens ? ` · ${run.lens}` : ""}</strong>
        <span className={`mm-chip mm-chip-${run.status}`}>{status}</span>
        <span className="mm-muted mm-small">{Math.round(elapsed)}m</span>
      </div>
      {node ? (
        <button type="button" className="mm-textlink mm-ellipsis mm-small" onClick={() => props.onReveal(node.id)} title="Show on the map">
          {run.template} · {node.title || "Untitled"}
        </button>
      ) : null}
      {run.last_output ? <div className="mm-muted mm-small mm-ellipsis" title={run.last_output}>{run.last_output}</div> : null}
      {run.error && run.status === "failed" ? <div className="mm-err mm-small">{run.error}</div> : null}
      <div className="mm-row mm-drawer-actions">
        {canApprove(run, ctl.engine.participant) ? (
          <button type="button" className="mm-btn mm-btn-xs mm-btn-primary" disabled={busy} onClick={async () => {
            setBusy(true);
            await approveRun(ctl, run);
            setBusy(false);
          }} data-testid="mm-dock-approve">Approve</button>
        ) : null}
        {run.task_id && ctl.host.kind === "kandev" ? (
          <button type="button" className="mm-btn mm-btn-xs" onClick={() => ctl.host.navigate(`/t/${encodeURIComponent(run.task_id!)}`)}>Open task</button>
        ) : null}
        {canStop(run) ? (
          <>
            <button type="button" className={`mm-btn mm-btn-xs${ctl.followingRun === run.id ? " mm-btn-on" : ""}`} onClick={() => {
              ctl.followingRun = ctl.followingRun === run.id ? null : run.id;
              ctl.bump();
            }}>Follow</button>
            <button type="button" className="mm-btn mm-btn-xs" disabled={busy} onClick={async () => {
              setBusy(true);
              await ctl.host.api.stopRun(run.id).then(() => ctl.host.toast("Agent stopped.", "info"), () => ctl.host.toast("Could not stop the agent.", "error"));
              setBusy(false);
            }}>Stop</button>
          </>
        ) : null}
      </div>
    </div>
  );
}

function ActivityFeed(props: { ctl: MapController }) {
  const events = props.ctl.engine.activity;
  if (!events.length) return <p className="mm-muted mm-pad">No activity yet.</p>;
  return (
    <div className="mm-activity-feed">
      {events.map((e, i) => (
        <div key={i} className="mm-activity-item">
          <span className="mm-muted mm-small">{new Date(e.ts).toLocaleTimeString()}</span>
          <span>{e.summary}</span>
        </div>
      ))}
    </div>
  );
}

function ParticipantsList(props: { ctl: MapController }) {
  const participants = [...props.ctl.engine.participants.values()];
  return (
    <div className="mm-participants-list">
      {participants.map((p) => (
        <div key={p.id} className="mm-row mm-participant-row">
          <span className="mm-avatar-dot" style={{ background: p.color }} />
          <span className="mm-grow">{p.name}{p.kind === "agent" && p.harness ? ` (${p.harness})` : ""}{p.is_host ? " · host" : ""}</span>
          <span className="mm-muted mm-small">{p.online ? "Online" : "Offline"}</span>
        </div>
      ))}
    </div>
  );
}
