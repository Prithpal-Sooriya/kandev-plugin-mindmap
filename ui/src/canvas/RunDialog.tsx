// "Run agent…" dialog: one agent (template), a swarm (lenses × profiles),
// the explorer, the gardener or execute. Rules live in runForm.ts.
import { ContractError, LENSES, type AgentProfile, type Lens, type RepoInfo, type Run, type RunMode } from "../contract";
import { h, React } from "../runtime";
import type { MapController } from "./controller";
import { Icon, ICONS } from "./icons";
import {
  KIND_TITLE, LENS_LABEL, MAX_SWARM, RUN_TEMPLATES, TEMPLATE_LABEL, buildRequest, describeDraft, hasErrors, initialDraft,
  nextLens, unavailableReason, validateDraft, type RunDraft, type RunKind, type RunTemplate,
} from "./runForm";

const LAST_PROFILE_KEY = "mindmap.lastProfile";

function lastProfile(): string | undefined {
  try {
    return localStorage.getItem(LAST_PROFILE_KEY) ?? undefined;
  } catch {
    return undefined;
  }
}

function rememberProfile(id: string): void {
  try {
    localStorage.setItem(LAST_PROFILE_KEY, id);
  } catch {
    // storage blocked: the next dialog starts from the default
  }
}

const KIND_HELP: Record<RunKind, string> = {
  run: "One agent works on this node and its branch.",
  swarm: "Several agents, each with a different lens, each in its own sub-branch.",
  explore: "An agent grows the map from here on its own, within the budget, and may start sub-agents.",
  garden: "An agent tidies the whole map: merges duplicates, fixes structure. It only proposes changes.",
  execute: "An agent carries out this action in a repository.",
};

export function RunDialog(props: { ctl: MapController; kind: RunKind; nodeId: string; onClose: () => void; onStarted?: (runs: Run[]) => void }) {
  const { ctl } = props;
  const node = ctl.node(props.nodeId);
  const map = ctl.engine.confirmed.map;
  const [profiles, setProfiles] = React.useState(null as AgentProfile[] | null);
  const [repos, setRepos] = React.useState([] as RepoInfo[]);
  const [loadErr, setLoadErr] = React.useState("");
  const [draft, setDraft] = React.useState(null as RunDraft | null);
  const [busy, setBusy] = React.useState(false);
  const [submitErr, setSubmitErr] = React.useState("");
  const [touched, setTouched] = React.useState(false);

  React.useEffect(() => {
    let live = true;
    ctl.host.api.profiles().then((ps) => {
      if (!live) return;
      setProfiles(ps);
      if (node) {
        const d = initialDraft(props.kind, {
          node, rootId: ctl.rootId, profiles: ps, preferredProfileId: map?.settings.default_profile_id ?? lastProfile(), mapRepoId: map?.repo_id,
        });
        if (map?.settings.default_mode && props.kind !== "garden") d.mode = map.settings.default_mode;
        setDraft(d);
      }
    }, (e) => live && setLoadErr(e instanceof ContractError ? e.message : "Could not load agent profiles."));
    if (props.kind === "execute" || props.kind === "run") {
      ctl.host.api.repos(ctl.mapId).then((rs) => live && setRepos(rs), () => {});
    }
    return () => {
      live = false;
    };
  }, []);

  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        props.onClose();
      }
    };
    document.addEventListener("keydown", onKey, true);
    return () => document.removeEventListener("keydown", onKey, true);
  }, []);

  const set = (patch: Partial<RunDraft>) => setDraft((d: RunDraft | null) => (d ? { ...d, ...patch } : d));
  const errors = draft && profiles ? validateDraft(draft, profiles) : {};
  const blocked = node && profiles ? unavailableReason(props.kind, node, profiles) : null;

  const submit = async () => {
    setTouched(true);
    if (!draft || !profiles || blocked || hasErrors(errors)) return;
    setBusy(true);
    setSubmitErr("");
    try {
      const r = buildRequest(draft);
      const runs = r.kind === "swarm" ? await ctl.host.api.startSwarm(ctl.mapId, r.req) : await ctl.host.api.startRun(ctl.mapId, r.req);
      if (draft.kind !== "swarm") rememberProfile(draft.profileId);
      const failed = runs.filter((x) => x.status === "failed");
      if (failed.length) ctl.host.toast(`Could not start the agent: ${failed[0]!.error ?? "launch failed"}`, "error");
      else ctl.host.toast(draft.kind === "swarm" ? `Swarm started with ${runs.length - 1} agents.` : "Agent started.", "success");
      props.onStarted?.(runs);
      props.onClose();
    } catch (e) {
      setSubmitErr(e instanceof ContractError ? e.message : "Could not start the run. Try again.");
    } finally {
      setBusy(false);
    }
  };

  const title = KIND_TITLE[props.kind];
  const body = !node ? <p className="mm-muted">That node no longer exists.</p>
    : loadErr ? <p className="mm-warn">{loadErr}</p>
    : !profiles || !draft ? <p className="mm-muted">Loading agent profiles…</p>
    : blocked ? <p className="mm-warn">{blocked}</p>
    : (
      <div className="mm-run-form">
        <p className="mm-muted mm-small">{KIND_HELP[props.kind]}</p>
        <p className="mm-run-target">
          <span className="mm-muted">{props.kind === "garden" ? "Map" : "Node"}</span>{" "}
          <strong className="mm-ellipsis">{props.kind === "garden" ? map?.title ?? "" : node.title}</strong>
        </p>
        {props.kind === "run" ? (
          <label className="mm-field">
            <span>Template</span>
            <select className="mm-input" value={draft.template} onChange={(e: Event) => set({ template: (e.target as HTMLSelectElement).value as RunTemplate })} data-testid="mm-run-template">
              {RUN_TEMPLATES.map((t) => <option key={t} value={t}>{TEMPLATE_LABEL[t]}</option>)}
            </select>
          </label>
        ) : null}
        {props.kind === "swarm" ? (
          <SwarmEditor draft={draft} profiles={profiles} onChange={(swarm) => set({ swarm })} error={touched ? errors.swarm : undefined} />
        ) : (
          <label className="mm-field">
            <span>Agent profile</span>
            <ProfileSelect profiles={profiles} value={draft.profileId} onChange={(profileId) => set({ profileId })} testId="mm-run-profile" />
            {touched && errors.profile ? <em className="mm-err">{errors.profile}</em> : null}
          </label>
        )}
        <label className="mm-field">
          <span>Goal{draft.kind === "run" && draft.template === "custom" ? "" : " (optional)"}</span>
          <textarea
            className="mm-input mm-run-goal"
            rows={3}
            value={draft.goal}
            maxLength={4000}
            placeholder={props.kind === "garden" ? "Anything to focus on?" : "What should the agent find or produce?"}
            onInput={(e: Event) => set({ goal: (e.target as HTMLTextAreaElement).value })}
            data-testid="mm-run-goal"
          />
          {touched && errors.goal ? <em className="mm-err">{errors.goal}</em> : null}
        </label>
        <div className="mm-row mm-run-mode" role="radiogroup" aria-label="Mode">
          {(["apply", "propose"] as RunMode[]).map((m) => (
            <label key={m} className={`mm-radio${props.kind === "garden" && m === "apply" ? " mm-disabled" : ""}`}>
              <input type="radio" name="mm-run-mode" value={m} checked={draft.mode === m} disabled={props.kind === "garden" && m === "apply"} onChange={() => set({ mode: m })} />
              {m === "apply" ? "Apply directly" : "Propose for review"}
            </label>
          ))}
        </div>
        {props.kind === "execute" || (props.kind === "run" && repos.length) ? (
          <label className="mm-field">
            <span>Repository{props.kind === "execute" ? "" : " (optional)"}</span>
            <select className="mm-input" value={draft.repoId} onChange={(e: Event) => set({ repoId: (e.target as HTMLSelectElement).value })}>
              <option value="">{props.kind === "execute" ? "Map default / none" : "None"}</option>
              {repos.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
            </select>
          </label>
        ) : null}
        <details className="mm-run-budget" open={props.kind === "explore"}>
          <summary>Budget{props.kind === "swarm" ? " (per agent)" : ""}</summary>
          <div className="mm-run-budget-grid">
            {([["nodes", "New nodes"], ["depth", "Depth"], ["subagents", "Sub-agents"], ["minutes", "Minutes"]] as const).map(([k, label]) => (
              <label key={k} className="mm-field">
                <span>{label}</span>
                <input className="mm-input" inputMode="numeric" value={draft.budget[k]} onInput={(e: Event) => set({ budget: { ...draft.budget, [k]: (e.target as HTMLInputElement).value } })} />
              </label>
            ))}
          </div>
          {touched && errors.budget ? <em className="mm-err">{errors.budget}</em> : null}
        </details>
        {submitErr ? <p className="mm-err">{submitErr}</p> : null}
      </div>
    );

  return (
    <div className="mm-palette-backdrop" onPointerDown={(e: PointerEvent) => e.target === e.currentTarget && props.onClose()}>
      <div className="mm-palette mm-dialog" role="dialog" aria-label={title} data-testid="mm-run-dialog">
        <header className="mm-insp-head">
          <Icon d={ICONS.bolt} />
          <strong>{title}</strong>
          <span className="mm-spacer" />
          <button type="button" className="mm-icon-btn" onClick={props.onClose} aria-label="Close"><Icon d={ICONS.close} /></button>
        </header>
        <div className="mm-dialog-body mm-scroll">{body}</div>
        <footer className="mm-dialog-foot">
          <button type="button" className="mm-btn" onClick={props.onClose}>Cancel</button>
          <button
            type="button"
            className="mm-btn mm-btn-primary"
            disabled={busy || !draft || !profiles || !!blocked || (touched && hasErrors(errors))}
            onClick={submit}
            data-testid="mm-run-start"
          >
            {busy ? "Starting…" : draft && profiles ? describeDraft(draft, profiles) : "Start"}
          </button>
        </footer>
      </div>
    </div>
  );
}

function ProfileSelect(props: { profiles: AgentProfile[]; value: string; onChange: (id: string) => void; testId?: string }) {
  return (
    <select className="mm-input" value={props.value} onChange={(e: Event) => props.onChange((e.target as HTMLSelectElement).value)} data-testid={props.testId}>
      {props.value ? null : <option value="">Choose a profile…</option>}
      {props.profiles.map((p) => <option key={p.id} value={p.id}>{p.name} ({p.harness})</option>)}
    </select>
  );
}

function SwarmEditor(props: { draft: RunDraft; profiles: AgentProfile[]; onChange: (s: RunDraft["swarm"]) => void; error?: string }) {
  const slots = props.draft.swarm;
  const setSlot = (i: number, patch: Partial<RunDraft["swarm"][number]>) => props.onChange(slots.map((s, j) => (j === i ? { ...s, ...patch } : s)));
  return (
    <div className="mm-field">
      <span>Agents ({slots.length}/{MAX_SWARM})</span>
      {slots.map((s, i) => (
        <div className="mm-row mm-swarm-row" key={i}>
          <select className="mm-input" value={s.lens} onChange={(e: Event) => setSlot(i, { lens: (e.target as HTMLSelectElement).value as Lens })} aria-label="Lens">
            {LENSES.map((l) => <option key={l} value={l}>{LENS_LABEL[l]}</option>)}
          </select>
          <ProfileSelect profiles={props.profiles} value={s.profileId} onChange={(profileId) => setSlot(i, { profileId })} />
          <button type="button" className="mm-icon-btn" onClick={() => props.onChange(slots.filter((_, j) => j !== i))} aria-label="Remove agent"><Icon d={ICONS.close} /></button>
        </div>
      ))}
      {slots.length < MAX_SWARM ? (
        <button type="button" className="mm-btn mm-btn-sm" onClick={() => props.onChange([...slots, { lens: nextLens(slots), profileId: slots[slots.length - 1]?.profileId ?? props.profiles[0]?.id ?? "" }])}>
          <Icon d={ICONS.plus} /> Add agent
        </button>
      ) : null}
      {props.error ? <em className="mm-err">{props.error}</em> : null}
    </div>
  );
}
