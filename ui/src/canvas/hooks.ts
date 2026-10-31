// Hooks built only from the HostReact primitives (no useSyncExternalStore,
// no memo): the host gives us useState/useEffect/useLayoutEffect/useMemo/
// useCallback/useRef and nothing else.
import { React } from "../runtime";

interface Subscribable {
  subscribe(fn: () => void): () => void;
  version: number;
}

/**
 * Re-renders when the store changes, coalesced to one render per animation
 * frame (rAF-batched: a burst of WS frames costs one render).
 */
export function useStore(store: Subscribable | null): number {
  const [, setTick] = React.useState(0);
  React.useEffect(() => {
    if (!store) return;
    let raf = 0;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const flush = () => {
      if (raf) cancelAnimationFrame(raf);
      if (timer) clearTimeout(timer);
      raf = 0;
      timer = null;
      setTick((t: number) => t + 1);
    };
    const unsub = store.subscribe(() => {
      // Inside a user input event (a keystroke in the editor, a click), render
      // now: React flushes discrete-event updates before the browser paints, so
      // the keystroke shows in the next frame. Remote updates are rAF-batched.
      const ev = (globalThis as { event?: Event }).event;
      if (ev && /^(key|input|pointer|mouse|click|dblclick|beforeinput|composition)/.test(ev.type)) {
        flush();
        return;
      }
      if (raf) return;
      raf = requestAnimationFrame(flush);
      // rAF is paused in hidden/throttled frames; don't let state go stale there.
      timer = setTimeout(flush, 100);
    });
    return () => {
      unsub();
      if (raf) cancelAnimationFrame(raf);
      if (timer) clearTimeout(timer);
    };
  }, [store]);
  return store?.version ?? 0;
}

/** Async data with loading/error state. */
export function useAsync<T>(fn: () => Promise<T>, deps: readonly unknown[]): { data: T | null; error: string | null; loading: boolean; reload: () => void } {
  const [state, setState] = React.useState({ data: null as T | null, error: null as string | null, loading: true });
  const [nonce, setNonce] = React.useState(0);
  React.useEffect(() => {
    let live = true;
    setState((s: typeof state) => ({ ...s, loading: true }));
    fn().then(
      (data) => live && setState({ data, error: null, loading: false }),
      (e: unknown) => live && setState({ data: null, error: e instanceof Error ? e.message : String(e), loading: false }),
    );
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce]);
  return { ...state, reload: () => setNonce((n: number) => n + 1) };
}
