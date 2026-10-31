import { h, React, Fragment } from "../runtime";
import type { MapController } from "./controller";
import type { Run, Participant, ActivityEvent } from "../contract";
import { Icon, ICONS } from "./icons";
import { HarnessBadge, AvatarStack } from "./agent";

export function AgentsDock(props: { ctl: MapController; onOpenTask?: (runId: string) => void; onStopRun?: (runId: string) => void; onMessageRun?: (runId: string) => void; onAnswerQuestion?: (runId: string) => void; }) {
  const { ctl } = props;
  const open = ctl.dockOpen;
  const runs = [...ctl.engine.runs.values()];
  const activeRuns = runs.filter(r => r.status === "running" || r.status === "queued" || r.status === "waiting" || r.status === "review");
  const running = activeRuns.filter(r => r.status === "running" || r.status === "queued").length;
  
  return (
    <>
      <button 
        className={`mm-fab ${running > 0 ? "mm-fab-running" : ""}`}
        onClick={() => { ctl.dockOpen = true; ctl.bump(); }}
        aria-label="Agents dock"
        title="Agents dock"
      >
        <Icon d={ICONS.panel} />
        {activeRuns.length > 0 && <span className={`mm-fab-badge ${running > 0 ? "mm-fab-pulse" : ""}`}>{activeRuns.length}</span>}
      </button>
      {open && <AgentsDrawer ctl={ctl} onClose={() => { ctl.dockOpen = false; ctl.bump(); }} />}
    </>
  );
}

function AgentsDrawer(props: { ctl: MapController; onClose: () => void }) {
  const { ctl } = props;
  const [tab, setTab] = React.useState<"runs" | "activity" | "participants">("runs");

  return (
    <aside className="mm-drawer" aria-label="Agents drawer">
      <header className="mm-drawer-head">
        <strong>Agents & Activity</strong>
        <span className="mm-spacer" />
        <button type="button" className="mm-icon-btn" onClick={props.onClose} aria-label="Close drawer"><Icon d={ICONS.close} /></button>
      </header>
      <div className="mm-drawer-tabs">
        <button className={tab === "runs" ? "mm-on" : ""} onClick={() => setTab("runs")}>Runs</button>
        <button className={tab === "activity" ? "mm-on" : ""} onClick={() => setTab("activity")}>Activity</button>
        <button className={tab === "participants" ? "mm-on" : ""} onClick={() => setTab("participants")}>Participants</button>
      </div>
      <div className="mm-drawer-body mm-scroll">
        {tab === "runs" && <RunsList ctl={ctl} />}
        {tab === "activity" && <ActivityFeed ctl={ctl} />}
        {tab === "participants" && <ParticipantsList ctl={ctl} />}
      </div>
    </aside>
  );
}

function RunsList(props: { ctl: MapController }) {
  const { ctl } = props;
  const runs = [...ctl.engine.runs.values()];
  
  if (runs.length === 0) {
    return <p className="mm-muted mm-pad">No agents running.</p>;
  }

  // Group as a tree
  const rootRuns = runs.filter(r => !r.parent_run_id);
  const childrenMap = new Map<string, Run[]>();
  for (const r of runs) {
    if (r.parent_run_id) {
      const arr = childrenMap.get(r.parent_run_id) ?? [];
      arr.push(r);
      childrenMap.set(r.parent_run_id, arr);
    }
  }

  const renderRun = (run: Run, depth = 0) => {
    const children = childrenMap.get(run.id) ?? [];
    return (
      <div key={run.id} className="mm-drawer-run-group">
        <RunRow ctl={ctl} run={run} depth={depth} />
        {children.map(c => renderRun(c, depth + 1))}
      </div>
    );
  };

  return <div className="mm-drawer-runs">{rootRuns.map(r => renderRun(r))}</div>;
}

function RunRow(props: { ctl: MapController; run: Run; depth: number }) {
  const { ctl, run, depth } = props;
  const participant = ctl.engine.participants.get(run.participant_id);
  const color = participant?.color;
  
  const path = ctl.view.nodes.has(run.node_id) ? (() => { const p = []; let curr = ctl.view.nodes.get(run.node_id); while (curr) { p.unshift(curr); curr = curr.parent_id ? ctl.view.nodes.get(curr.parent_id) : undefined; } return p; })() : [];
  const pathText = path.map(n => n.title || "Untitled").join(" / ");

  const elapsed = Math.round((Date.now() - Date.parse(run.created_at)) / 60000);
  
  const handleFly = () => {
    // API to reveal
    const api = (window as any).__mm?.ctl?.api; // We'll need a better way to fly
  };

  return (
    <div className={`mm-drawer-run mm-depth-${Math.min(depth, 3)}`}>
      <div className="mm-run-header">
        <HarnessBadge harness={run.harness} color={color} title={participant?.name} />
        <strong>{run.profile_name ?? run.template}{run.lens ? ` · ${run.lens}` : ""}</strong>
        <span className={`mm-chip mm-chip-${run.status}`}>{run.status}</span>
        <span className="mm-muted mm-small">{elapsed}m</span>
      </div>
      <div className="mm-run-path" onClick={() => ctl.select(run.node_id)} title={pathText}>
        {pathText}
      </div>
      <div className="mm-run-budget mm-muted mm-small">
        {run.budget?.max_nodes ? `Nodes: ${run.usage?.nodes ?? 0}/${run.budget.max_nodes}` : null}
        {run.budget?.max_subagents ? ` Agents: ${run.usage?.subagents ?? 0}/${run.budget.max_subagents}` : null}
      </div>
      {run.last_output && <div className="mm-run-output mm-ellipsis" title={run.last_output}>{run.last_output}</div>}
      <div className="mm-run-actions">
        <button className="mm-btn mm-btn-xs" disabled>Open Task</button>
        <button className="mm-btn mm-btn-xs" disabled>Message</button>
        {run.status === "waiting" && <button className="mm-btn mm-btn-xs mm-btn-primary" disabled>Answer</button>}
        {run.status !== "stopped" && run.status !== "done" && run.status !== "failed" && <><button className={`mm-btn mm-btn-xs ${ctl.followingRun === run.id ? "mm-btn-on" : ""}`} onClick={() => { ctl.followingRun = ctl.followingRun === run.id ? null : run.id; ctl.bump(); }}>Follow</button>
        <button className="mm-btn mm-btn-xs" disabled>Stop</button></>}
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
      {participants.map(p => (
        <div key={p.id} className="mm-participant-row">
          <span className="mm-avatar" style={{ background: p.color }} />
          <span>{p.name} {p.kind === "agent" ? `(${p.harness})` : ""}</span>
          {p.online ? <span className="mm-online-dot" /> : <span className="mm-offline.summary">Offline</span>}
        </div>
      ))}
    </div>
  );
}
