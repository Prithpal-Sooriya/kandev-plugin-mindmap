// Static rendering for component tests (no @types/react-dom).
declare module "react-dom/server" {
  export function renderToStaticMarkup(node: unknown): string;
}
