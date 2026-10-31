// Share dialog, kill switch, and the "shared · N guests" chip. Host only for
// the controls; guests see the chip because they arrived through a share link.
import { ContractError, type ShareLink } from "../contract";
import type { HostAdapter } from "../host/adapter";
import { Fragment, h, React } from "../runtime";
import { encodeQR } from "./qr";
import { shareDialogModel, sharedLabel, type ShareProblem } from "./share";

export function ShareControls(props: { host: HostAdapter; mapId: string; guests: number }) {
  const { host, mapId } = props;
  const isGuest = host.kind === "standalone" && !host.canListMaps;
  const [link, setLink] = React.useState(null as ShareLink | null);
  const [open, setOpen] = React.useState(false);
  const [confirmKill, setConfirmKill] = React.useState(false);

  React.useEffect(() => {
    if (!host.canListMaps) return;
    let cancel = false;
    host.api.shareStatus(mapId).then((l) => {
      if (!cancel) setLink(l);
    }).catch((e: unknown) => {
      if (cancel || (e instanceof ContractError && (e.code === "not_found" || e.code === "not_implemented"))) return;
    });
    return () => { cancel = true; };
  }, [host, mapId]);

  const shared = link?.status === "active" || link?.status === "starting";
  const kill = async () => {
    if (!confirmKill) {
      setConfirmKill(true);
      return;
    }
    try {
      await host.api.killSharing();
      setLink(null);
      setConfirmKill(false);
      setOpen(false);
      host.toast("Sharing is off and guest sessions were revoked.", "success");
    } catch (e) {
      host.toast(e instanceof ContractError ? e.message : "Could not stop sharing.", "error");
    }
  };

  return h(Fragment, null,
    shared || isGuest ? h("span", { className: "mm-chip", "data-testid": "mm-shared" }, sharedLabel(props.guests)) : null,
    host.canListMaps ? h("button", { type: "button", className: "mm-btn mm-btn-sm", onClick: () => setOpen(true) }, "Share") : null,
    host.canListMaps ? h("button", {
      type: "button",
      className: `mm-btn mm-btn-sm${confirmKill ? " mm-btn-danger" : ""}`,
      onClick: () => void kill(),
      title: "Stop sharing every map, revoke guest sessions, and stop active runs",
    }, confirmKill ? "Stop everything?" : "Kill switch") : null,
    open ? h(ShareDialog, {
      host, mapId, link, onLink: setLink, onClose: () => setOpen(false),
    }) : null,
  );
}

function ShareDialog(props: {
  host: HostAdapter;
  mapId: string;
  link: ShareLink | null;
  onLink: (l: ShareLink | null) => void;
  onClose: () => void;
}) {
  const { host, mapId } = props;
  const [error, setError] = React.useState(null as ShareProblem | null);
  const [busy, setBusy] = React.useState(false);
  const model = shareDialogModel(props.link, error);

  const run = async (fn: () => Promise<ShareLink | null>) => {
    setBusy(true);
    setError(null);
    try {
      props.onLink(await fn());
    } catch (e) {
      if (e instanceof ContractError) setError({ code: e.code, message: e.message });
      else setError({ code: "unavailable", message: "Could not update sharing." });
    } finally {
      setBusy(false);
    }
  };

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(model.url);
      host.toast("Link copied.", "success");
    } catch {
      host.toast("Select the link and copy it.", "info");
    }
  };

  return h("div", { className: "mm-share-backdrop", onClick: props.onClose },
    h("div", {
      className: "mm-share", role: "dialog", "aria-label": "Share this map",
      onClick: (ev: Event) => ev.stopPropagation(),
    },
      h("header", { className: "mm-share-head" },
        h("strong", null, "Share this map"),
        h("button", { type: "button", className: "mm-icon-btn", onClick: props.onClose, "aria-label": "Close share dialog" }, "×"),
      ),
      h("p", { className: "mm-muted" }, "Anyone with the link can edit this map and start runs, same as you. The link reaches only this map."),
      model.phase === "missing" ? h("div", { className: "mm-share-missing" },
        h("p", null, "The tunnel program is not installed."),
        h("code", { className: "mm-code" }, model.install),
      ) : null,
      model.phase === "error" ? h("p", { className: "mm-share-error" }, model.message) : null,
      model.phase === "shared" ? h("div", { className: "mm-share-link" },
        h(QR, { text: model.url }),
        h("input", { className: "mm-input", readOnly: true, value: model.url, "aria-label": "Share link", onFocus: (ev: Event) => (ev.target as HTMLInputElement).select() }),
        h("div", { className: "mm-share-actions" },
          h("button", { type: "button", className: "mm-btn mm-btn-sm", onClick: () => void copy() }, "Copy link"),
          h("button", { type: "button", className: "mm-btn mm-btn-sm", disabled: busy, onClick: () => void run(() => host.api.rotateShare(mapId)) }, "Rotate"),
          h("button", { type: "button", className: "mm-btn mm-btn-sm mm-btn-danger", disabled: busy, onClick: () => void run(async () => { await host.api.stopShare(mapId); return null; }) }, "Stop"),
        ),
      ) : h("div", { className: "mm-share-actions" },
        h("button", { type: "button", className: "mm-btn mm-btn-sm mm-btn-primary", disabled: busy, onClick: () => void run(() => host.api.startShare(mapId)) }, busy ? "Starting…" : "Share"),
      ),
    ),
  );
}

function QR(props: { text: string }) {
  let matrix: boolean[][] | null = null;
  try {
    matrix = encodeQR(props.text).matrix;
  } catch {
    matrix = null;
  }
  if (!matrix) return null;
  const n = matrix.length;
  const d: string[] = [];
  for (let y = 0; y < n; y++) for (let x = 0; x < n; x++) if (matrix[y]![x]) d.push(`M${x} ${y}h1v1h-1z`);
  const q = 2;
  return h("svg", {
    className: "mm-qr", viewBox: `${-q} ${-q} ${n + q * 2} ${n + q * 2}`, role: "img", "aria-label": "QR code for the share link",
  },
    h("rect", { x: -q, y: -q, width: n + q * 2, height: n + q * 2, fill: "#fff" }),
    h("path", { d: d.join(""), fill: "#111" }),
  );
}
