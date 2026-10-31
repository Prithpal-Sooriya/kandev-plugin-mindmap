// Test harness: real SyncEngine + Connection talking to the in-memory
// FakeServer through a fake WebSocket and fake long-poll endpoints.
import type { ServerMessage } from "../ui/src/contract";
import { SyncEngine } from "../ui/src/core/sync";
import { FakeServer, type FakeConn } from "../ui/src/fake/server";
import { Connection, type MapEndpoints, type WsLike } from "../ui/src/transport/connection";

export const flush = async (n = 6) => {
  for (let i = 0; i < n; i++) await new Promise((r) => setTimeout(r, 0));
};

export class FakeSocket implements WsLike {
  readyState = 0;
  onopen: ((ev: unknown) => void) | null = null;
  onclose: ((ev: unknown) => void) | null = null;
  onerror: ((ev: unknown) => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  conn: FakeConn | null = null;
  /** When true, server → client frames are dropped (simulates a lost ack before a disconnect). */
  blackhole = false;

  constructor(private server: FakeServer, private mapId: string, fail: boolean) {
    setTimeout(() => {
      if (fail) {
        this.readyState = 3;
        this.onclose?.({});
        return;
      }
      this.readyState = 1;
      this.conn = server.connect(mapId, (m: ServerMessage) => {
        const data = JSON.stringify(m);
        setTimeout(() => {
          if (this.readyState === 1 && !this.blackhole) this.onmessage?.({ data });
        }, 0);
      });
      this.onopen?.({});
    }, 0);
  }

  send(data: string): void {
    if (this.readyState !== 1 || !this.conn) throw new Error("socket not open");
    this.server.receive(this.conn, JSON.parse(data));
  }

  close(): void {
    if (this.readyState === 3) return;
    this.readyState = 3;
    if (this.conn) this.server.disconnect(this.conn);
    setTimeout(() => this.onclose?.({}), 0);
  }
}

export interface Client {
  engine: SyncEngine;
  conn: Connection;
  sockets: FakeSocket[];
  /** Make subsequent socket opens fail (to force long-poll). */
  failWs: boolean;
  errors: string[];
  syncCalls: number;
  submitCalls: number;
}

export function makeClient(server: FakeServer, mapId: string, opts: { name?: string; failWs?: boolean; participantId?: string } = {}): Client {
  const engine = new SyncEngine(mapId);
  const client: Client = {
    engine, conn: null as unknown as Connection, sockets: [], failWs: !!opts.failWs, errors: [], syncCalls: 0, submitCalls: 0,
  };
  const who = () => engine.participant?.id ?? opts.participantId ?? server.hostOf(mapId).id;
  const endpoints: MapEndpoints = {
    wsUrl: async (m) => `ws://fake/${m}`,
    sync: async (m, cursor, waitS, _sub, signal, clientId) => {
      client.syncCalls++;
      if (signal.aborted) throw new DOMException("aborted", "AbortError");
      return server.sync(m, who(), cursor || undefined, Math.min(waitS * 1000, 30), clientId);
    },
    submit: async (m, req) => {
      client.submitCalls++;
      return server.submitHttp(m, who(), req);
    },
  };
  client.conn = new Connection(engine, endpoints, {
    clientId: `tab-${Math.random().toString(36).slice(2, 8)}`,
    name: opts.name,
    ws: () => {
      const s = new FakeSocket(server, mapId, client.failWs);
      client.sockets.push(s);
      return s;
    },
    // Backoffs run at once; long timers (ping, WS retry) never fire in tests.
    setTimeout: ((fn: () => void, ms: number) => (ms > 5000 ? 0 : globalThis.setTimeout(fn, 0))) as unknown as typeof setTimeout,
    wsRetryMs: 1_000_000,
    strict: true,
  });
  engine.onError = (e) => client.errors.push(e.code);
  return client;
}

export function lastSocket(c: Client): FakeSocket {
  return c.sockets.at(-1)!;
}
