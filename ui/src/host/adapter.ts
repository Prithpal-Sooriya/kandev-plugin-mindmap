// The canvas depends only on this interface. Two implementations:
//   ui/src/kandev/adapter.ts      in-Kandev bundle (host React, webhooks, loopback WS)
//   ui/src/standalone/adapter.ts  guest web app (own React, /m/<token>/ paths)
import type {
  AgentProfile, CreateMapRequestShape, Infer, MapSummary, MindMap, Op, OutputLine, Participant, Proposal,
  ReviewProposalRequest, Run, StartRunRequest,
} from "../contract";
import type { MapEndpoints, WsFactory } from "../transport/connection";

export type CreateMapRequest = Infer<typeof CreateMapRequestShape>;
export type Theme = "light" | "dark";
export type ToastKind = "info" | "success" | "error";

/** The `/api/*` routes of protocol §4 that the canvas calls. */
export interface MapApi {
  listMaps(): Promise<MapSummary[]>;
  createMap(req: CreateMapRequest): Promise<MindMap>;
  renameMap?(mapId: string, title: string): Promise<void>;
  profiles(): Promise<AgentProfile[]>;
  reviewProposal(proposalId: string, req: ReviewProposalRequest): Promise<Proposal>;
  startRun(mapId: string, req: StartRunRequest): Promise<Run[]>;
  /** Server-side per-author undo (protocol §13): reverts the caller's latest batch. */
  undo(mapId: string): Promise<{ ops: Op[]; rev: number }>;
  getOps?(mapId: string, from: number, to: number): Promise<{ ops: Op[] }>;
  forkMap?(mapId: string, rev: number): Promise<MindMap>;
}

export interface HostAdapter {
  readonly kind: "kandev" | "standalone";
  /** Workspace for new maps (Kandev: the active workspace). */
  workspaceId(): string | undefined;
  theme(): Theme;
  onThemeChange(fn: (t: Theme) => void): () => void;
  toast(message: string, kind?: ToastKind): void;
  /** Navigate within the host app (Kandev route) or the guest app. */
  navigate(path: string): void;
  /** Current map from the URL (?map=), if any. */
  initialMapId(): string | null;
  setMapInUrl(mapId: string | null): void;
  /** The current human participant, once known (session webhook or guest hello). */
  participant(): Participant | null;
  /** Whether this host may list and create maps (local token) or only sees one map (share token). */
  canListMaps: boolean;
  /** Guest identity, if the host needs the user to pick one before connecting. */
  guestIdentity?(): { name: string; color: string } | null;
  api: MapApi;
  endpoints: MapEndpoints;
  ws?: WsFactory;
  /** Resolves an asset to a URL that the page's CSP allows (asset webhook blob URL in Kandev). */
  assetUrl(mapId: string, assetId: string): Promise<string>;
  /**
   * Live agent output for a run. Kandev can read the session directly; guests
   * rely on `output` frames over the plugin WS (the canvas falls back to those).
   */
  useLiveOutput?(run: Run | null): OutputLine[] | null;
}
