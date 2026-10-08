"use client";
import { useCallback, useEffect, useRef, useState } from "react";

// One fetch at a time per hook. Failed polling retains the last good snapshot.
export function usePoll<T>(load: (signal: AbortSignal) => Promise<T>, key: string) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState("");
  const [updated, setUpdated] = useState<string | null>(null);
  const [nonce, setNonce] = useState(0);
  const loader = useRef(load); loader.current = load;
  const refresh = useCallback(() => setNonce((value) => value + 1), []);
  useEffect(() => {
    setData(null); setError(""); setUpdated(null);
  }, [key]);
  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>; let failures = 0;
    async function poll() {
      try {
        const value = await loader.current(controller.signal);
        if (controller.signal.aborted) return;
        setData(value); setError(""); setUpdated(new Date().toISOString()); failures = 0;
      } catch (cause) {
        if (controller.signal.aborted) return;
        setError(cause instanceof Error ? cause.message : "Unable to refresh traffic data."); failures++;
      }
      if (!controller.signal.aborted) timer = setTimeout(poll, Math.min(1000 * 2 ** failures, 8000));
    }
    void poll();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [key, nonce]);
  return { data, error, updated, refresh };
}
