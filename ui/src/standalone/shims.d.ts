// The standalone app only touches React through the HostReact surface (see
// ui/src/runtime.ts), so it needs no @types/react: these declarations cover
// the two entry-point imports.
declare module "react" {
  import type { HostReact } from "@kandev/plugin-sdk";
  const React: HostReact;
  export = React;
}

declare module "react-dom/client" {
  export function createRoot(el: Element): { render(node: unknown): void; unmount(): void };
}

declare module "*.css";
