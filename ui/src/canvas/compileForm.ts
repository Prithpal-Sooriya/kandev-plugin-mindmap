// State and rules of the "Compile to docs" dialog, kept free of React so the
// vitest suite can check them. The dialog edits a CompileDraft and turns it
// into a CompileRequest (protocol §4).
import type { AgentProfile, CompileRequest, MapNode, RunMode } from "../contract";
import { pickProfile } from "./runForm";

/** Compile the selected branch or the whole map. */
export type CompileScope = "branch" | "map";
/** Where the files go: the map's repository (on the task's branch) or the export folder. */
export type CompileTarget = "repo" | "dir";

/** Mirrors the server's MaxCompileSections. */
export const MAX_COMPILE_SECTIONS = 24;

export interface CompileDraft {
  scope: CompileScope;
  /** The selected node; the map root when scope is "map". */
  nodeId: string;
  rootId: string;
  profileId: string;
  mode: RunMode;
  target: CompileTarget;
  repoId: string;
  slug: string;
}

export interface CompileContext {
  node: Pick<MapNode, "id" | "kind" | "title">;
  rootId: string;
  profiles: AgentProfile[];
  preferredProfileId?: string;
  mapRepoId?: string;
  defaultMode?: RunMode;
}

export function initialCompileDraft(ctx: CompileContext): CompileDraft {
  const onRoot = ctx.node.id === ctx.rootId;
  return {
    scope: onRoot ? "map" : "branch",
    nodeId: ctx.node.id,
    rootId: ctx.rootId,
    profileId: pickProfile(ctx.profiles, ctx.preferredProfileId),
    mode: ctx.defaultMode ?? "apply",
    target: ctx.mapRepoId ? "repo" : "dir",
    repoId: ctx.mapRepoId ?? "",
    slug: "",
  };
}

/** The node the compile starts from. */
export function compileRoot(d: CompileDraft): string {
  return d.scope === "map" ? d.rootId : d.nodeId;
}

/** How many section agents a compile of `nodeId` starts: one per non-doc child, or one for a leaf. */
export function sectionCount(nodeId: string, children: (id: string) => Pick<MapNode, "kind">[]): number {
  return Math.max(1, children(nodeId).filter((c) => c.kind !== "doc").length);
}

/** Why the compile cannot start, if it cannot. */
export function compileUnavailable(node: Pick<MapNode, "kind">, profiles: AgentProfile[], scope: CompileScope): string | null {
  if (!profiles.length) return "No agent profiles found. Add one in Kandev settings first.";
  if (scope === "branch" && node.kind === "doc") return "This is a compiled doc. Pick a branch of the map, or compile the whole map.";
  return null;
}

export type CompileErrors = Partial<Record<"profile" | "slug" | "repo" | "sections", string>>;

export function validateCompile(d: CompileDraft, profiles: AgentProfile[], sections: number): CompileErrors {
  const errs: CompileErrors = {};
  if (!profiles.some((p) => p.id === d.profileId)) errs.profile = "Pick an agent profile.";
  const slug = d.slug.trim();
  if (slug.length > 80) errs.slug = "Keep the name under 80 characters.";
  else if (slug && !/[a-z0-9]/i.test(slug)) errs.slug = "Use at least one letter or digit.";
  if (d.target === "repo" && !d.repoId) errs.repo = "Pick a repository, or write to the export folder.";
  if (sections > MAX_COMPILE_SECTIONS) {
    errs.sections = `This has ${sections} top-level branches. Compile at most ${MAX_COMPILE_SECTIONS} at once (pick a sub-branch).`;
  }
  return errs;
}

export function hasCompileErrors(e: CompileErrors): boolean {
  return Object.keys(e).length > 0;
}

export function buildCompileRequest(d: CompileDraft): CompileRequest {
  const req: CompileRequest = { node_id: compileRoot(d), profile_id: d.profileId, mode: d.mode, target: d.target };
  if (d.target === "repo" && d.repoId) req.repo_id = d.repoId;
  const slug = d.slug.trim();
  if (slug) req.slug = slug;
  return req;
}

/** The confirm button: "Compile 3 sections". */
export function describeCompile(sections: number): string {
  return `Compile ${sections} section${sections === 1 ? "" : "s"}`;
}

/** The file name a slug becomes, as the server does it (Slugify). */
export function slugPreview(slug: string, fallbackTitle: string): string {
  const s = (v: string) => v.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 48).replace(/-+$/, "");
  return s(slug) || s(fallbackTitle) || "mindmap";
}
