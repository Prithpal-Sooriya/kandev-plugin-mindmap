// A fake plugin loopback server for bridge tests.
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";

export interface Recorded {
  method: string;
  url: string;
  headers: IncomingMessage["headers"];
  body: unknown;
}

export type Handler = (req: Recorded, res: ServerResponse) => void | Promise<void>;

export const SECRET = "A".repeat(43);

export async function startFake(handler: Handler) {
  const calls: Recorded[] = [];
  const server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (c) => chunks.push(c));
    req.on("end", async () => {
      const raw = Buffer.concat(chunks).toString("utf8");
      const rec: Recorded = { method: req.method ?? "", url: req.url ?? "", headers: req.headers, body: raw ? JSON.parse(raw) : undefined };
      calls.push(rec);
      await handler(rec, res);
    });
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const port = (server.address() as AddressInfo).port;
  return {
    port,
    calls,
    runToken: `mmr1.${port}.${SECRET}`,
    personalToken: `mmp1.${port}.${SECRET}`,
    close: () => new Promise<void>((r) => server.close(() => r())),
  };
}

export function json(res: ServerResponse, status: number, body: unknown) {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}
