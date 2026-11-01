// Compiled doc details in the inspector: where the file goes, the map nodes
// it came from, its sub-docs, and the export actions (zip for everyone,
// write to disk for the host).
import { ContractError, type DocEntry, type DocTree, type MapNode } from "../contract";
import { h, React } from "../runtime";
import type { MapController } from "./controller";
import { Icon, ICONS } from "./icons";

/** The doc tree a doc node belongs to, if any. */
export function findDocTree(trees: DocTree[], nodeId: string): DocTree | null {
  const has = (e: DocEntry): boolean => e.node_id === nodeId || (e.children ?? []).some(has);
  return trees.find((t) => t.single?.node_id === nodeId || has(t.index)) ?? null;
}

/** The file a doc becomes, relative to the export folder (or docs/mindmaps/ in a repo). */
export function docFilePath(tree: Pick<DocTree, "slug" | "single">, node: Pick<MapNode, "id" | "doc">): string {
  if (tree.single?.node_id === node.id) return `${tree.slug}.md`;
  return `${tree.slug}/${node.doc?.path || "…"}`;
}

/** Saves a blob through a temporary link. */
export function saveBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.rel = "noopener";
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 30_000);
}

/** Downloads the doc tree containing `docId` (default the newest) as a zip. */
export async function downloadDocsZip(ctl: MapController, docId?: string, slug = "docs"): Promise<void> {
  try {
    saveBlob(await ctl.host.api.exportZip(ctl.mapId, docId), `${slug}.zip`);
  } catch (e) {
    ctl.host.toast(e instanceof ContractError ? e.message : "Could not build the zip.", "error");
  }
}

export function DocPanel(props: { ctl: MapController; node: MapNode }) {
  const { ctl, node } = props;
  const [tree, setTree] = React.useState(null as DocTree | null);
  const [busy, setBusy] = React.useState(false);
  const reload = () => ctl.host.api.docs(ctl.mapId).then((ts) => setTree(findDocTree(ts, node.id)), () => setTree(null));
  React.useEffect(() => {
    reload();
  }, [node.id, node.rev]);

  const subDocs = ctl.siblingsOf(node.id).filter((c) => c.kind === "doc");
  const sources = (node.doc?.source_ids ?? []).map((id) => ctl.node(id)).filter(Boolean) as MapNode[];
  const isHost = !!ctl.engine.participant?.is_host;
  const reveal = (id: string) => ctl.select(id);

  const writeToDisk = async () => {
    setBusy(true);
    try {
      const t = await ctl.host.api.exportDocs(ctl.mapId, node.id);
      setTree(t);
      ctl.host.toast(`Docs written to ${t.exported_to}`, "success");
    } catch (e) {
      ctl.host.toast(e instanceof ContractError ? e.message : "Could not write the docs.", "error");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="mm-field mm-doc-panel" data-testid="mm-doc-panel">
      <span>Compiled doc</span>
      <p className="mm-small">
        <Icon d={ICONS.doc} size={12} /> <code>{tree ? docFilePath(tree, node) : node.doc?.path}</code>
      </p>
      {tree?.exported_to ? <p className="mm-muted mm-small" data-testid="mm-doc-exported">On disk: <code>{tree.exported_to}</code></p> : null}
      {node.doc?.compiled_at ? <p className="mm-muted mm-small">Written {new Date(node.doc.compiled_at).toLocaleString()}</p> : null}
      {subDocs.length ? (
        <div>
          <span className="mm-muted mm-small">Sub-docs</span>
          <ul className="mm-doc-list">
            {subDocs.map((d) => <li key={d.id}><button type="button" className="mm-textlink" onClick={() => reveal(d.id)}>{d.title || d.doc?.path}</button></li>)}
          </ul>
        </div>
      ) : null}
      {sources.length ? (
        <div>
          <span className="mm-muted mm-small">From the map</span>
          <ul className="mm-doc-list">
            {sources.map((s) => <li key={s.id}><button type="button" className="mm-textlink" onClick={() => reveal(s.id)}>{s.title || "Untitled"}</button></li>)}
          </ul>
        </div>
      ) : null}
      <div className="mm-row">
        <button type="button" className="mm-btn mm-btn-xs" disabled={!tree} onClick={() => downloadDocsZip(ctl, node.id, tree?.slug)} data-testid="mm-doc-zip">
          <Icon d={ICONS.download} size={12} /> Download zip
        </button>
        {isHost ? (
          <button type="button" className="mm-btn mm-btn-xs" disabled={!tree || busy} onClick={writeToDisk} data-testid="mm-doc-write">
            {tree?.exported_to ? "Write again" : "Write to export folder"}
          </button>
        ) : null}
      </div>
    </div>
  );
}
