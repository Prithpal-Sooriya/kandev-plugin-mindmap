// "Compile to docs" dialog: scope (this branch or the whole map), profile,
// mode and where the files go. Rules live in compileForm.ts.
import { ContractError, type AgentProfile, type RepoInfo, type Run, type RunMode } from "../contract";
import { Fragment, h, React } from "../runtime";
import {
  buildCompileRequest, compileRoot, compileUnavailable, describeCompile, hasCompileErrors, initialCompileDraft, sectionCount, slugPreview,
  validateCompile, type CompileDraft, type CompileScope, type CompileTarget,
} from "./compileForm";
import type { MapController } from "./controller";
import { Icon, ICONS } from "./icons";

export interface CompileFormProps {
  draft: CompileDraft;
  profiles: AgentProfile[];
  repos: RepoInfo[];
  mapRepoId?: string;
  nodeTitle: string;
  mapTitle: string;
  sections: number;
  touched: boolean;
  set: (patch: Partial<CompileDraft>) => void;
}

/** The form body, separate from the data loading so tests can render it. */
export function CompileForm(props: CompileFormProps) {
  const { draft, profiles, set } = props;
  const errors = validateCompile(draft, profiles, props.sections);
  const title = draft.scope === "map" ? props.mapTitle : props.nodeTitle;
  const slug = slugPreview(draft.slug, title);
  const canRepo = !!props.mapRepoId || props.repos.length > 0;
  const onRoot = draft.nodeId === draft.rootId;
  return (
    <div className="mm-run-form" data-testid="mm-compile-form">
      <p className="mm-muted mm-small">
        One agent writes each top-level branch in parallel; big branches get sub-docs. Then one agent writes the overview and a single
        consolidated doc. The docs appear in the map under the branch.
      </p>
      <div className="mm-row mm-run-mode" role="radiogroup" aria-label="Scope">
        {(["branch", "map"] as CompileScope[]).map((s) => (
          <label key={s} className={`mm-radio${s === "branch" && onRoot ? " mm-disabled" : ""}`}>
            <input type="radio" name="mm-compile-scope" value={s} checked={draft.scope === s} disabled={s === "branch" && onRoot} onChange={() => set({ scope: s })} />
            {s === "branch" ? "This branch" : "Whole map"}
          </label>
        ))}
      </div>
      <p className="mm-run-target">
        <span className="mm-muted">{draft.scope === "map" ? "Map" : "Branch"}</span> <strong className="mm-ellipsis">{title}</strong>
        <span className="mm-muted"> · {props.sections} section{props.sections === 1 ? "" : "s"}</span>
      </p>
      {errors.sections ? <p className="mm-warn">{errors.sections}</p> : null}
      <label className="mm-field">
        <span>Agent profile</span>
        <select className="mm-input" value={draft.profileId} onChange={(e: Event) => set({ profileId: (e.target as HTMLSelectElement).value })} data-testid="mm-compile-profile">
          {draft.profileId ? null : <option value="">Choose a profile…</option>}
          {profiles.map((p) => <option key={p.id} value={p.id}>{p.name} ({p.harness})</option>)}
        </select>
        {props.touched && errors.profile ? <em className="mm-err">{errors.profile}</em> : null}
      </label>
      <div className="mm-row mm-run-mode" role="radiogroup" aria-label="Mode">
        {(["apply", "propose"] as RunMode[]).map((m) => (
          <label key={m} className="mm-radio">
            <input type="radio" name="mm-compile-mode" value={m} checked={draft.mode === m} onChange={() => set({ mode: m })} />
            {m === "apply" ? "Add docs directly" : "Propose docs for review"}
          </label>
        ))}
      </div>
      {canRepo ? (
        <div className="mm-field">
          <span>Write files to</span>
          <div className="mm-row mm-run-mode" role="radiogroup" aria-label="Target">
            {(["repo", "dir"] as CompileTarget[]).map((t) => (
              <label key={t} className="mm-radio">
                <input type="radio" name="mm-compile-target" value={t} checked={draft.target === t} onChange={() => set({ target: t, repoId: t === "repo" ? draft.repoId || props.mapRepoId || "" : draft.repoId })} />
                {t === "repo" ? "Repository" : "Export folder"}
              </label>
            ))}
          </div>
          {draft.target === "repo" ? (
            <select className="mm-input" value={draft.repoId} onChange={(e: Event) => set({ repoId: (e.target as HTMLSelectElement).value })} aria-label="Repository" data-testid="mm-compile-repo">
              {draft.repoId ? null : <option value="">Choose a repository…</option>}
              {props.mapRepoId && !props.repos.some((r) => r.id === props.mapRepoId) ? <option value={props.mapRepoId}>Map repository</option> : null}
              {props.repos.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
            </select>
          ) : null}
          {props.touched && errors.repo ? <em className="mm-err">{errors.repo}</em> : null}
        </div>
      ) : null}
      <label className="mm-field">
        <span>Name (optional)</span>
        <input className="mm-input" value={draft.slug} maxLength={80} placeholder={slugPreview("", title)} onInput={(e: Event) => set({ slug: (e.target as HTMLInputElement).value })} data-testid="mm-compile-slug" />
        {props.touched && errors.slug ? <em className="mm-err">{errors.slug}</em> : null}
      </label>
      <p className="mm-muted mm-small" data-testid="mm-compile-path">
        {draft.target === "repo"
          ? <>Files: <code>docs/mindmaps/{slug}.md</code> and <code>docs/mindmaps/{slug}/</code> on the agent's branch.</>
          : <>Files: <code>{slug}.md</code> and <code>{slug}/</code> in the export folder, plus a zip download.</>}
      </p>
    </div>
  );
}

export function CompileDialog(props: { ctl: MapController; nodeId: string; onClose: () => void; onStarted?: (runs: Run[]) => void }) {
  const { ctl } = props;
  const node = ctl.node(props.nodeId);
  const map = ctl.engine.confirmed.map;
  const [profiles, setProfiles] = React.useState(null as AgentProfile[] | null);
  const [repos, setRepos] = React.useState([] as RepoInfo[]);
  const [loadErr, setLoadErr] = React.useState("");
  const [draft, setDraft] = React.useState(null as CompileDraft | null);
  const [busy, setBusy] = React.useState(false);
  const [submitErr, setSubmitErr] = React.useState("");
  const [touched, setTouched] = React.useState(false);

  React.useEffect(() => {
    let live = true;
    ctl.host.api.profiles().then((ps) => {
      if (!live || !node || !ctl.rootId) return;
      setProfiles(ps);
      setDraft(initialCompileDraft({
        node, rootId: ctl.rootId, profiles: ps, preferredProfileId: map?.settings.default_profile_id, mapRepoId: map?.repo_id, defaultMode: map?.settings.default_mode,
      }));
    }, (e) => live && setLoadErr(e instanceof ContractError ? e.message : "Could not load agent profiles."));
    ctl.host.api.repos(ctl.mapId).then((rs) => live && setRepos(rs), () => {});
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

  const set = (patch: Partial<CompileDraft>) => setDraft((d: CompileDraft | null) => (d ? { ...d, ...patch } : d));
  const sections = draft ? sectionCount(compileRoot(draft), (id) => ctl.siblingsOf(id)) : 0;
  const errors = draft && profiles ? validateCompile(draft, profiles, sections) : {};
  const blocked = node && profiles && draft ? compileUnavailable(node, profiles, draft.scope) : null;

  const submit = async () => {
    setTouched(true);
    if (!draft || !profiles || blocked || hasCompileErrors(errors)) return;
    setBusy(true);
    setSubmitErr("");
    try {
      const runs = await ctl.host.api.compile(ctl.mapId, buildCompileRequest(draft));
      const held = runs.some((r) => r.waiting_reason === "approval");
      const failed = runs.filter((r) => r.status === "failed");
      if (held) ctl.host.toast("Compile is waiting for the host to approve it.", "info");
      else if (failed.length) ctl.host.toast(`${failed.length} section agent${failed.length > 1 ? "s" : ""} could not start: ${failed[0]!.error ?? "launch failed"}`, "error");
      else ctl.host.toast(`Compiling ${runs.length - 1} section${runs.length === 2 ? "" : "s"}.`, "success");
      props.onStarted?.(runs);
      props.onClose();
    } catch (e) {
      setSubmitErr(e instanceof ContractError ? e.message : "Could not start the compile. Try again.");
    } finally {
      setBusy(false);
    }
  };

  const body = !node ? <p className="mm-muted">That node no longer exists.</p>
    : loadErr ? <p className="mm-warn">{loadErr}</p>
    : !profiles || !draft ? <p className="mm-muted">Loading agent profiles…</p>
    : (
      <>
        {blocked ? <p className="mm-warn">{blocked}</p> : null}
        <CompileForm draft={draft} profiles={profiles} repos={repos} mapRepoId={map?.repo_id} nodeTitle={node.title} mapTitle={map?.title ?? ""}
          sections={sections} touched={touched} set={set} />
        {submitErr ? <p className="mm-err">{submitErr}</p> : null}
      </>
    );

  return (
    <div className="mm-palette-backdrop" onPointerDown={(e: PointerEvent) => e.target === e.currentTarget && props.onClose()}>
      <div className="mm-palette mm-dialog" role="dialog" aria-label="Compile to docs" data-testid="mm-compile-dialog">
        <header className="mm-insp-head">
          <Icon d={ICONS.doc} />
          <strong>Compile to docs</strong>
          <span className="mm-spacer" />
          <button type="button" className="mm-icon-btn" onClick={props.onClose} aria-label="Close"><Icon d={ICONS.close} /></button>
        </header>
        <div className="mm-dialog-body mm-scroll">{body}</div>
        <footer className="mm-dialog-foot">
          <button type="button" className="mm-btn" onClick={props.onClose}>Cancel</button>
          <button
            type="button"
            className="mm-btn mm-btn-primary"
            disabled={busy || !draft || !profiles || !!blocked || (touched && hasCompileErrors(errors))}
            onClick={submit}
            data-testid="mm-compile-start"
          >
            {busy ? "Starting…" : describeCompile(sections)}
          </button>
        </footer>
      </div>
    </div>
  );
}
