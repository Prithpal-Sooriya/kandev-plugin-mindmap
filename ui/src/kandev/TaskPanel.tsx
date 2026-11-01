// Kandev task panel for mind map runs: the node the agent works on, its
// branch, the run status, and a link back to the map.
import type { PluginTaskPanelProps } from "@kandev/plugin-sdk";
import { ContractError, type NodeRef, type TaskRunInfo } from "../contract";
import { HarnessBadge } from "../canvas/agent";
import { KindIcon } from "../canvas/icons";
import type { HostAdapter } from "../host/adapter";
import { h, React } from "../runtime";

const ACTIVE = new Set(["queued", "running", "waiting", "review"]);
const STALE_MS = 15_000;

/**
 * Which tasks are map runs. `visible()` is synchronous, so it answers from
 * this cache and refreshes it in the background; before the first answer it
 * shows the panel (the panel itself says when a task is not a run).
 */
export class RunTaskCache {
  private ids: Set<string> | null = null;
  private fetchedAt = 0;
  private inflight: Promise<void> | null = null;

  constructor(private readonly load: () => Promise<string[]>) {}

  has(taskId: string): boolean {
    if (Date.now() - this.fetchedAt > STALE_MS) void this.refresh();
    return this.ids === null || this.ids.has(taskId);
  }

  add(taskId: string): void {
    this.ids?.add(taskId);
  }

  refresh(): Promise<void> {
    if (!this.inflight) {
      this.fetchedAt = Date.now();
      this.inflight = this.load()
        .then((ids) => void (this.ids = new Set(ids)), () => {})
        .finally(() => (this.inflight = null));
    }
    return this.inflight;
  }
}

export function mapLink(mapId: string, nodeId: string): string {
  return `/mindmap?map=${encodeURIComponent(mapId)}&node=${encodeURIComponent(nodeId)}`;
}

export function TaskPanel(props: PluginTaskPanelProps & { host: HostAdapter; cache: RunTaskCache }) {
  const { host, taskId } = props;
  const [info, setInfo] = React.useState(null as TaskRunInfo | null);
  const [err, setErr] = React.useState("");
  const [tick, setTick] = React.useState(0);

  React.useEffect(() => {
    let live = true;
    host.api.taskRun(taskId).then((i) => {
      if (!live) return;
      setInfo(i);
      setErr("");
      props.cache.add(taskId);
    }, (e) => {
      if (!live) return;
      setInfo(null);
      setErr(e instanceof ContractError && e.code === "not_found" ? "This task is not a mind map run." : "Could not load the mind map run.");
    });
    return () => {
      live = false;
    };
  }, [taskId, tick]);

  // Poll while the run is live so status and the branch stay current.
  const active = !!info && ACTIVE.has(info.run.status);
  React.useEffect(() => {
    if (!active) return;
    const t = setInterval(() => setTick((n: number) => n + 1), 5000);
    return () => clearInterval(t);
  }, [active]);

  if (err) return <div className="mm-task-panel mm-muted">{err}</div>;
  if (!info) return <div className="mm-task-panel mm-muted">Loading…</div>;
  const r = info.run;
  const node = info.branch[0];
  return (
    <div className="mm-root mm-task-panel" data-testid="mm-task-panel">
      <div className="mm-row">
        <HarnessBadge harness={r.harness} />
        <strong className="mm-ellipsis">{info.map_title}</strong>
        <span className="mm-spacer" />
        <span className={`mm-chip mm-chip-${r.status}`}>{r.status}{r.waiting_reason ? ` (${r.waiting_reason})` : ""}</span>
      </div>
      <p className="mm-muted mm-small">
        {r.template}{r.lens ? ` · ${r.lens}` : ""} · {r.mode} · {r.usage.nodes}/{r.budget.max_nodes ?? "∞"} nodes
      </p>
      {info.path.length ? (
        <p className="mm-crumbs mm-small">{info.path.map((p) => p.title).join(" › ")}</p>
      ) : null}
      <ul className="mm-branch">
        {info.branch.map((n: NodeRef) => (
          <li key={n.id} style={{ paddingLeft: `${(n.depth ?? 0) * 14}px` }} className={n.id === node?.id ? "mm-branch-root" : ""}>
            <KindIcon kind={n.kind} size={12} />
            <span className="mm-ellipsis">{n.title}</span>
            {n.status && n.status !== "idle" ? <span className={`mm-chip mm-chip-${n.status}`}>{n.status}</span> : null}
          </li>
        ))}
        {info.more ? <li className="mm-muted">…more in the map</li> : null}
      </ul>
      {r.summary ? <p className="mm-small">{r.summary}</p> : null}
      <div className="mm-row">
        <button type="button" className="mm-btn mm-btn-sm mm-btn-primary" onClick={() => host.navigate(mapLink(r.map_id, r.node_id))}>Open in map</button>
        {ACTIVE.has(r.status) ? (
          <button type="button" className="mm-btn mm-btn-sm" onClick={() => host.api.stopRun(r.id).then(() => setTick((n: number) => n + 1), () => host.toast("Could not stop the agent.", "error"))}>Stop</button>
        ) : null}
      </div>
    </div>
  );
}
