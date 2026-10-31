// Host-supplied React runtime. The bundle ships no React of its own: every
// element is created with the host's factory so plugin components share the
// app's React instance, context, and dev tools.
import type { HostReact, PluginHostApi } from "@kandev/plugin-sdk";

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export let h: (...args: any[]) => any = () => {
  throw new Error("kandev-plugin-mindmap: host runtime not bound");
};
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export let Fragment: any = null;
export let React: HostReact;
export let host: PluginHostApi;

export function bindHost(api: PluginHostApi): void {
  host = api;
  React = api.React;
  h = api.jsx;
  Fragment = api.React.Fragment;
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
