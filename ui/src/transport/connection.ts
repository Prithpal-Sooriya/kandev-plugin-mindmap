// One live connection per open map: WebSocket first (protocol §5), long-poll
// fallback (§6) when the socket can't open, and periodic attempts to get back
// onto the socket. The SyncEngine never knows which transport is in use.
import {
  CONTRACT_VERSION, LIMITS, validate, ServerMessageShape, type ClientMessage, type Op, type Presence,
  type ServerMessage, type SubmitRequest, type SyncResponse,
} from "../contract";
import type { SyncEngine } from "../core/sync";

/** Minimal WebSocket surface, so tests can inject a fake. */
export interface WsLike {
  readonly readyState: number;
  onopen: ((ev: unknown) => void) | null;
  onclose: ((ev: unknown) => void) | null;
  onerror: ((ev: unknown) => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  send(data: string): void;
  close(): void;
}
export type WsFactory = (url: string) => WsLike;

/** Where a map's server lives. Implemented per host (Kandev webhooks + loopback, or the guest path). */
export interface MapEndpoints {
  /** WebSocket URL for the map, or null when only the fallback is possible. May refresh tokens. */
  wsUrl(mapId: string): Promise<string | null>;
  /** GET sync (§6). `clientId` keeps your own submitted ops out of the stream (§13). */
  sync(mapId: string, cursor: string, waitS: number, subscribe: string[], signal: AbortSignal, clientId: string): Promise<SyncResponse>;
  /** POST submit/ops (§6). The answer's cursor is always "": keep polling with the last sync cursor. */
  submit(mapId: string, req: SubmitRequest): Promise<SyncResponse>;
}

export type ConnState = "connecting" | "ws" | "longpoll" | "offline";

export interface ConnectionOptions {
  clientId: string;
  name?: string;
  color?: string;
  ws?: WsFactory;
  /** Consecutive socket failures before switching to long-poll. */
  wsFailuresBeforeFallback?: number;
  /** While on long-poll, how often to try the socket again (ms). */
  wsRetryMs?: number;
  pingMs?: number;
  /** Validate inbound frames against the contract shapes (dev builds). */
  strict?: boolean;
  setTimeout?: typeof setTimeout;
  clearTimeout?: typeof clearTimeout;
}

const OPEN = 1;

export class Connection {
  state: ConnState = "connecting";
  onState: (s: ConnState) => void = () => {};
  private ws: WsLike | null = null;
  private wsFailures = 0;
  private closed = false;
  private pollAbort: AbortController | null = null;
  private polling = false;
  private timers = new Set<ReturnType<typeof setTimeout>>();
  private subscriptions = new Set<string>();
  private needResend = false;
  private presenceQueued: Presence | null = null;
  private presenceTimer: ReturnType<typeof setTimeout> | null = null;
  private lastPresenceAt = 0;
  private opts: Required<Omit<ConnectionOptions, "name" | "color" | "ws">> & ConnectionOptions;

  constructor(
    private engine: SyncEngine,
    private endpoints: MapEndpoints,
    opts: ConnectionOptions,
  ) {
    this.opts = {
      wsFailuresBeforeFallback: 2, wsRetryMs: 30_000, pingMs: 20_000, strict: false,
      setTimeout: globalThis.setTimeout.bind(globalThis), clearTimeout: globalThis.clearTimeout.bind(globalThis), ...opts,
    };
    engine.submit = (ops) => this.sendOps(ops);
    engine.onResync = () => this.resync();
  }

  start(): void {
    this.closed = false;
    void this.openWs();
  }

  stop(): void {
    this.closed = true;
    this.ws?.close();
    this.ws = null;
    this.pollAbort?.abort();
    for (const t of this.timers) this.opts.clearTimeout(t);
    this.timers.clear();
    this.setState("offline");
  }

  private later(ms: number, fn: () => void): void {
    const t = this.opts.setTimeout(() => {
      this.timers.delete(t);
      fn();
    }, ms);
    this.timers.add(t);
  }

  private setState(s: ConnState): void {
    if (this.state === s) return;
    this.state = s;
    this.onState(s);
  }

  // --- WebSocket -------------------------------------------------------------------

  private async openWs(): Promise<void> {
    if (this.closed || !this.opts.ws) return this.fallback();
    let url: string | null = null;
    try {
      url = await this.endpoints.wsUrl(this.engine.mapId);
    } catch {
      url = null;
    }
    if (this.closed) return;
    if (!url) return this.wsFailed();
    let ws: WsLike;
    try {
      ws = this.opts.ws(url);
    } catch {
      return this.wsFailed();
    }
    this.ws = ws;
    let opened = false;
    ws.onopen = () => {
      opened = true;
      this.sendRaw(this.hello());
    };
    ws.onmessage = (ev) => {
      const msg = this.parse(ev.data);
      if (!msg) return;
      if (msg.type === "welcome") {
        this.wsFailures = 0;
        this.stopPolling();
        this.setState("ws");
        for (const run of this.subscriptions) this.sendRaw({ type: "output.subscribe", payload: { run_id: run } });
        this.schedulePing(ws);
      }
      this.engine.handle(msg);
      if (msg.type === "welcome") this.resendPending();
      if (msg.type === "error" && msg.payload.fatal) ws.close();
    };
    ws.onerror = () => {};
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.ws = null;
      if (this.closed) return;
      if (!opened) return this.wsFailed();
      // A socket that was up dropped: reconnect quickly, without counting it as a failure.
      this.setState("connecting");
      this.later(500, () => void this.openWs());
    };
  }

  private wsFailed(): void {
    this.wsFailures++;
    if (this.wsFailures >= this.opts.wsFailuresBeforeFallback) {
      this.fallback();
      this.later(this.opts.wsRetryMs, () => {
        if (!this.closed && this.state === "longpoll") void this.openWs();
      });
      return;
    }
    this.later(Math.min(10_000, 400 * 2 ** this.wsFailures), () => void this.openWs());
  }

  private schedulePing(ws: WsLike): void {
    this.later(this.opts.pingMs, () => {
      if (this.ws !== ws || ws.readyState !== OPEN) return;
      this.sendRaw({ type: "ping", payload: {} });
      this.schedulePing(ws);
    });
  }

  private hello(): ClientMessage {
    return {
      type: "hello",
      payload: {
        contract: CONTRACT_VERSION, client_id: this.opts.clientId, ...this.engine.resumeFields(),
        ...(this.opts.name ? { name: this.opts.name } : {}), ...(this.opts.color ? { color: this.opts.color } : {}),
      },
    };
  }

  private sendRaw(msg: ClientMessage): boolean {
    if (this.ws && this.ws.readyState === OPEN) {
      this.ws.send(JSON.stringify(msg));
      return true;
    }
    return false;
  }

  private parse(data: unknown): ServerMessage | null {
    try {
      const msg = JSON.parse(String(data)) as ServerMessage;
      if (this.opts.strict) {
        const errs = validate(ServerMessageShape, msg);
        if (errs.length) console.warn("mindmap: frame does not match the contract", errs.slice(0, 5), msg);
      }
      return msg;
    } catch {
      return null;
    }
  }

  /** Asks for a fresh snapshot (hello without since_rev). */
  resync(): void {
    // A fresh socket says hello without since_rev (the engine dropped hasSnapshot) and gets a snapshot.
    if (this.ws && this.ws.readyState === OPEN) this.ws.close();
    else if (this.state === "longpoll") this.restartPolling("");
  }

  // --- ops and presence --------------------------------------------------------------

  private sendOps(ops: Op[]): void {
    if (!ops.length) return;
    if (this.state === "ws") {
      for (let i = 0; i < ops.length; i += LIMITS.opsPerBatch) {
        this.sendRaw({ type: "submit", payload: { ops: ops.slice(i, i + LIMITS.opsPerBatch) } });
      }
    } else if (this.state === "longpoll") void this.postOps(ops);
    // Otherwise they stay in engine.pending and go out after the next welcome.
  }

  private resendPending(): void {
    // Keep batches together: a batch must stay atomic on resubmit.
    const pending = this.engine.resendQueue();
    const groups: Op[][] = [];
    for (const op of pending) {
      const last = groups.at(-1);
      if (last && op.batch_id && last[0]!.batch_id === op.batch_id) last.push(op);
      else groups.push([op]);
    }
    let cur: Op[] = [];
    const flush = () => {
      if (cur.length) this.sendOps(cur);
      cur = [];
    };
    for (const g of groups) {
      if (cur.length + g.length > LIMITS.opsPerBatch) flush();
      cur.push(...g);
    }
    flush();
  }

  private async postOps(ops: Op[], presence?: Presence): Promise<void> {
    try {
      const res = await this.endpoints.submit(this.engine.mapId, {
        client_id: this.opts.clientId, ops, ...(presence ? { presence } : {}),
      });
      for (const m of res.messages) this.engine.handle(m);
    } catch {
      // Network trouble: the ops stay pending and are resent after the next good poll.
      this.needResend = true;
    }
  }

  /** Presence is throttled to 15/s on the socket and 1/s on long-poll (§5, §6). */
  updatePresence(p: Presence): void {
    this.presenceQueued = p;
    if (this.presenceTimer) return;
    const gap = this.state === "longpoll" ? 1000 : 66;
    const wait = Math.max(0, this.lastPresenceAt + gap - Date.now());
    this.presenceTimer = this.opts.setTimeout(() => {
      this.presenceTimer = null;
      const q = this.presenceQueued;
      this.presenceQueued = null;
      if (!q) return;
      this.lastPresenceAt = Date.now();
      if (this.state === "ws") this.sendRaw({ type: "presence.update", payload: { presence: q } });
      else if (this.state === "longpoll") void this.postOps([], q);
    }, wait);
  }

  subscribeOutput(runId: string, on: boolean): void {
    if (on) this.subscriptions.add(runId);
    else this.subscriptions.delete(runId);
    if (this.state === "ws") this.sendRaw({ type: on ? "output.subscribe" : "output.unsubscribe", payload: { run_id: runId } });
    else if (this.state === "longpoll") this.restartPolling(this.engine.cursor);
  }

  // --- long-poll -------------------------------------------------------------------

  private fallback(): void {
    if (this.closed || this.state === "longpoll") return;
    this.setState("longpoll");
    // Both transports share cursor and rev: start from what we have.
    this.restartPolling(this.engine.hasSnapshot ? this.engine.cursor : "");
  }

  private stopPolling(): void {
    this.polling = false;
    this.pollAbort?.abort();
    this.pollAbort = null;
  }

  private restartPolling(cursor: string): void {
    this.stopPolling();
    this.engine.cursor = cursor;
    this.polling = true;
    void this.pollLoop();
  }

  private async pollLoop(): Promise<void> {
    const ctrl = new AbortController();
    this.pollAbort = ctrl;
    let firstRound = true;
    let backoff = 500;
    while (this.polling && !ctrl.signal.aborted && !this.closed) {
      try {
        const res = await this.endpoints.sync(this.engine.mapId, this.engine.cursor, firstRound ? 0 : 20, [...this.subscriptions], ctrl.signal, this.opts.clientId);
        if (ctrl.signal.aborted) return;
        for (const m of res.messages) this.engine.handle(m);
        this.engine.cursor = res.cursor;
        if (firstRound || this.needResend) {
          firstRound = false;
          this.needResend = false;
          this.resendPending();
        }
        backoff = 500;
      } catch {
        if (ctrl.signal.aborted) return;
        await new Promise((r) => this.later(backoff, () => r(null)));
        backoff = Math.min(10_000, backoff * 2);
      }
    }
  }
}
