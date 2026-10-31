// React runtime binding. The canvas code never imports "react": it uses `h`,
// `Fragment` and `React` from here. The in-Kandev bundle binds the host's
// React instance (no bundled React, no react-dom); the standalone guest app
// binds its own React 19. Only the primitives in HostReact are used, so the
// same components run in both.
import type { HostReact } from "@kandev/plugin-sdk";

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export let h: (...args: any[]) => any = () => {
  throw new Error("kandev-plugin-mindmap: React runtime not bound");
};
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export let Fragment: any = null;
export let React: HostReact;

/** Binds the React instance and element factory used by every component. */
export function bindReact(react: HostReact, jsx?: HostReact["createElement"]): void {
  React = react;
  h = jsx ?? react.createElement;
  Fragment = react.Fragment;
}

// JSX.IntrinsicElements typing without depending on @types/react.
declare global {
  // eslint-disable-next-line @typescript-eslint/no-namespace
  namespace JSX {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    type Element = any;
    interface IntrinsicAttributes {
      key?: string | number;
    }
    interface IntrinsicElements {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      [name: string]: any;
    }
  }
}
