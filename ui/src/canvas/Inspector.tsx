// Minimal inspector: title, markdown body, kind, tags, read-only sources and
// assets, and the pending proposal items on this node. The richer inspector
// (live output, replay) is Phase 2b.
import { NODE_KINDS, type NodeKind } from "../contract";
import { h, React } from "../runtime";
import { AvatarStack, HarnessBadge, ringFor } from "./agent";
import type { MapController } from "./controller";
import { Icon, ICONS, KIND_LABEL, KindIcon } from "./icons";
import { renderMarkdown } from "./markdown";

export function Inspector(props: { ctl: MapController; onClose: () => void }) {
  const { ctl } = props;
  const id = ctl.selection;
  const node = ctl.node(id);
  const [bodyEdit, setBodyEdit] = React.useState(false);
  const [title, setTitle] = React.useState(node?.title ?? "");
  const [tags, setTags] = React.useState((node?.tags ?? []).join(", "));
  const [lightboxUrl, setLightboxUrl] = React.useState<string | null>(null);
  React.useEffect(() => {
    setTitle(node?.title ?? "");
    setTags((node?.tags ?? []).join(", "));
    setBodyEdit(false);
  }, [id]);
  React.useEffect(() => {
    // Remote edits refresh the fields we are not typing in.
    const active = document.activeElement as HTMLElement | null;
    if (!active?.classList.contains("mm-insp-title")) setTitle(node?.title ?? "");
    if (!active?.classList.contains("mm-insp-tags")) setTags((node?.tags ?? []).join(", "));
  }, [node?.title, (node?.tags ?? []).join(",")]);

  if (!node || !id) {
    return (
      <aside className="mm-inspector" aria-label="Inspector">
        <header className="mm-insp-head"><strong>Inspector</strong><button type="button" className="mm-icon-btn" onClick={props.onClose} aria-label="Close inspector"><Icon d={ICONS.close} /></button></header>
        <p className="mm-muted mm-pad">Select a node to see its details.</p>
      {lightboxUrl && (
        <div className="mm-lightbox" onClick={() => setLightboxUrl(null)}>
          <img src={lightboxUrl} alt="Full size" />
          <button type="button" className="mm-icon-btn mm-lightbox-close"><Icon d={ICONS.close} /></button>
        </div>
      )}
    </aside>
    );
  }
  const lockedBy = ctl.lockedBy(node.id, "body_md");
  const runs = ctl.display.runsByNode.get(node.id) ?? [];
  const ring = ringFor(node, runs);
  const agents = runs.map((r) => ctl.engine.participants.get(r.participant_id)).filter(Boolean) as NonNullable<ReturnType<typeof ctl.engine.participants.get>>[];
  const proposals = [...ctl.engine.proposals.values()].filter((p) => (p.status === "open" || p.status === "ready") && p.items.some((i) => i.status === "pending" && i.node_ids.includes(node.id)));
  const author = ctl.engine.participants.get(node.author.participant_id);

  return (
    <aside className="mm-inspector" aria-label="Inspector" data-testid="mm-inspector">
      <header className="mm-insp-head">
        <KindIcon kind={node.kind} size={14} />
        <strong>{KIND_LABEL[node.kind]}</strong>
        {ring ? <span className={`mm-chip mm-chip-${ring.status}`}>{ring.status}</span> : null}
        <span className="mm-spacer" />
        <button type="button" className="mm-icon-btn" onClick={props.onClose} aria-label="Close inspector"><Icon d={ICONS.close} /></button>
      </header>
      <div className="mm-insp-body mm-scroll">
        <label className="mm-field">
          <span>Title</span>
          <input
            className="mm-input mm-insp-title"
            value={title}
            maxLength={200}
            onInput={(e: Event) => setTitle((e.target as HTMLInputElement).value)}
            onFocus={() => ctl.lock(node.id, "title")}
            onBlur={() => {
              ctl.unlock();
              ctl.setTitle(node.id, title);
            }}
            onKeyDown={(e: KeyboardEvent) => {
              if (e.key === "Enter") (e.target as HTMLInputElement).blur();
              if (e.key === "Escape") {
                setTitle(node.title);
                (e.target as HTMLInputElement).blur();
              }
            }}
          />
        </label>
        <div className="mm-row">
          <label className="mm-field mm-grow">
            <span>Kind</span>
            <select className="mm-input" value={node.kind} disabled={node.kind === "doc"} onChange={(e: Event) => ctl.update(node.id, { kind: (e.target as HTMLSelectElement).value as NodeKind })}>
              {NODE_KINDS.filter((k) => k !== "doc" || node.kind === "doc").map((k) => <option key={k} value={k}>{KIND_LABEL[k]}</option>)}
            </select>
          </label>
          <label className="mm-field">
            <span>Confidence</span>
            <input type="range" className="mm-input-range" min="0" max="100" value={Math.round((node.confidence ?? 0.5) * 100)} onChange={(e: any) => ctl.update(node.id, { confidence: parseInt(e.target.value) / 100 })} />
            <div className="mm-conf-big">{node.confidence !== undefined ? Math.round(node.confidence * 100) + "%" : "-"}</div>
          </label>
        </div>
        <label className="mm-field">
          <span>Tags</span>
          <input
            className="mm-input mm-insp-tags"
            value={tags}
            placeholder="comma, separated"
            onInput={(e: Event) => setTags((e.target as HTMLInputElement).value)}
            onBlur={() => {
              const next = tags.split(",").map((t: string) => t.trim()).filter(Boolean);
              if (next.join(",") !== (node.tags ?? []).join(",")) ctl.setTags(node.id, next);
            }}
            onKeyDown={(e: KeyboardEvent) => e.key === "Enter" && (e.target as HTMLInputElement).blur()}
          />
        </label>
        <div className="mm-field">
          <span className="mm-row">Body{lockedBy ? <em className="mm-muted"> · {lockedBy} is editing…</em> : null}
            <span className="mm-spacer" />
            {!bodyEdit ? <button type="button" className="mm-btn mm-btn-xs" disabled={!!lockedBy} onClick={() => { setBodyEdit(true); ctl.lock(node.id, "body_md"); }}>Edit</button> : null}
          </span>
          {bodyEdit ? (
            <BodyEditor initial={node.body_md ?? ""} onDone={(text) => {
              setBodyEdit(false);
              ctl.unlock();
              if (text !== null && text !== (node.body_md ?? "")) {
                if (text.trim()) ctl.update(node.id, { body_md: text });
                else ctl.edit([{ type: "node.update", payload: { id: node.id, clear: ["body_md"] } }]);
              }
            }} />
          ) : node.body_md ? (
            <div className="mm-md" dangerouslySetInnerHTML={{ __html: renderMarkdown(node.body_md) }} />
          ) : <p className="mm-muted">No notes yet.</p>}
        </div>
        {node.sources?.length ? (
          <div className="mm-field">
            <span>Sources ({node.sources.length})</span>
            <ul className="mm-sources">
              {node.sources.map((s) => (
                <li key={s.url}>
                  <a href={s.url} target="_blank" rel="noopener noreferrer">{s.title || s.url}</a>
                  {s.quote ? <blockquote>{s.quote}</blockquote> : null}
                  {s.accessed_at ? <div className="mm-muted mm-small">Accessed: {new Date(s.accessed_at).toLocaleDateString()}</div> : null}
                </li>
              ))}
            </ul>
          </div>
        ) : node.kind === "research" ? <p className="mm-warn">Uncited research: no sources yet.</p> : null}
        {node.assets?.length ? (
          <div className="mm-field">
            <span>Attachments ({node.assets.length})</span>
            <div className="mm-assets">{node.assets.map((a) => <AssetThumb key={a.id} ctl={ctl} id={a.id} kind={a.kind} caption={a.caption ?? a.filename ?? a.mime} onClick={(url) => setLightboxUrl(url)} />)}</div>
          </div>
        ) : null}
        {runs.length ? (
          <div className="mm-field">
            <span>Agents</span>
            {runs.map((r) => (
              <div className="mm-run-row" key={r.id}>
                <HarnessBadge harness={r.harness} color={ctl.engine.participants.get(r.participant_id)?.color} />
                <span>{r.profile_name ?? r.template}{r.lens ? ` · ${r.lens}` : ""}</span>
                <span className={`mm-chip mm-chip-${r.status}`}>{r.status}{r.waiting_reason ? ` (${r.waiting_reason})` : ""}</span>
                {r.last_output ? <div className="mm-muted mm-ellipsis">{r.last_output}</div> : null}
                <LiveOutput ctl={ctl} run={r} />
              </div>
            ))}
            <AvatarStack people={agents} size="sm" />
          </div>
        ) : null}
        {proposals.map((p) => (
          <div className="mm-field mm-proposal-box" key={p.id}>
            <span>Proposed changes{p.summary ? `: ${p.summary}` : ""}</span>
            <ul className="mm-prop-items">
              {p.items.filter((i) => i.status === "pending" && i.node_ids.includes(node.id)).map((i) => (
                <li key={i.op.id}>
                  <code>{i.op.type}</code>
                  <button type="button" className="mm-btn mm-btn-xs mm-btn-primary" onClick={() => ctl.review(p.id, { accept: [i.op.id] })}>Accept</button>
                  <button type="button" className="mm-btn mm-btn-xs" onClick={() => ctl.review(p.id, { reject: [i.op.id] })}>Reject</button>
                </li>
              ))}
            </ul>
          </div>
        ))}
        <TimeTravel ctl={ctl} />

        <p className="mm-muted mm-small">Added by {author?.name ?? "someone"} · rev {node.rev}</p>
      </div>
      {lightboxUrl && (
        <div className="mm-lightbox" onClick={() => setLightboxUrl(null)}>
          <img src={lightboxUrl} alt="Full size" />
          <button type="button" className="mm-icon-btn mm-lightbox-close"><Icon d={ICONS.close} /></button>
        </div>
      )}
    </aside>
  );
}

function BodyEditor(props: { initial: string; onDone: (text: string | null) => void }) {
  const ref = React.useRef(null as HTMLTextAreaElement | null);
  React.useLayoutEffect(() => ref.current?.focus(), []);
  return (
    <div>
      <textarea
        ref={ref}
        className="mm-input mm-body-editor mm-scroll"
        defaultValue={props.initial}
        rows={10}
        onKeyDown={(e: KeyboardEvent) => {
          if (e.key === "Escape") props.onDone(null);
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) props.onDone((e.target as HTMLTextAreaElement).value);
        }}
        onBlur={(e: FocusEvent) => props.onDone((e.target as HTMLTextAreaElement).value)}
        aria-label="Body (markdown)"
      />
      <p className="mm-muted mm-small">Markdown. ⌘Enter or click away to save, Esc to cancel.</p>
    </div>
  );
}

function AssetThumb(props: { ctl: MapController; id: string; kind: string; caption: string; onClick?: (url: string) => void }) {
  const [url, setUrl] = React.useState(null as string | null);
  React.useEffect(() => {
    let live = true;
    props.ctl.host.assetUrl(props.ctl.mapId, props.id).then((u) => live && setUrl(u), () => {});
    return () => {
      live = false;
    };
  }, [props.id]);
  return (
    <figure className="mm-asset">
      {props.kind === "image" && url ? <img src={url} alt={props.caption} onClick={() => props.onClick?.(url)} style={{ cursor: "zoom-in" }} /> : <div className="mm-asset-file"><Icon d={ICONS.image} /></div>}
      <figcaption>{props.caption}</figcaption>
    </figure>
  );
}


function LiveOutput(props: { ctl: MapController; run: import("../contract").Run }) {
  const { ctl, run } = props;
  const fallback = ctl.engine.output.get(run.id) ?? [];
  const lines = ctl.host.useLiveOutput?.(run) ?? fallback;
  
  if (!lines.length) return null;
  return (
    <div className="mm-run-live-output mm-scroll">
      {lines.map((l, i) => (
        <div key={i} className="mm-output-line">
          {l.kind === "tool" ? <strong>{l.text}</strong> : <span>{l.text}</span>}
        </div>
      ))}
    </div>
  );
}

function TimeTravel(props: { ctl: MapController }) {
  const { ctl } = props;
  if (ctl.replayRev === null) {
    return (
      <div className="mm-field">
        <button type="button" className="mm-btn mm-btn-primary" onClick={() => ctl.startReplay()}>Time Travel Replay</button>
      </div>
    );
  }
  
  const minRev = 1;
  const maxRev = ctl.engine.confirmed.rev;
  
  return (
    <div className="mm-field mm-time-travel">
      <div className="mm-row">
        <strong>Time Travel</strong>
        <span className="mm-spacer" />
        <button type="button" className="mm-icon-btn" onClick={() => ctl.stopReplay()}><Icon d={ICONS.close} /></button>
      </div>
      <input 
        type="range" 
        className="mm-input-range" 
        min={minRev} 
        max={maxRev} 
        value={ctl.replayRev} 
        onChange={(e: any) => ctl.setReplayRev(parseInt(e.target.value))} 
      />
      <div className="mm-row mm-small mm-muted">
        <span>Rev {ctl.replayRev} / {maxRev}</span>
        <span className="mm-spacer" />
        <button 
          type="button" 
          className="mm-btn mm-btn-xs" 
          onClick={async () => {
            if (ctl.host.api.forkMap) {
              const m = await ctl.host.api.forkMap(ctl.mapId, ctl.replayRev!);
              ctl.host.navigate(`/mindmap?map=${m.id}`);
            }
          }}
        >
          Fork from here
        </button>
      </div>
    </div>
  );
}
