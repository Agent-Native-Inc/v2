import { useCallback, useEffect, useState, type RefObject } from "react";

type VideoRef = RefObject<HTMLVideoElement | null>;

// True when a key goes to a text field, so page shortcuts leave it alone.
export const isTyping = (e: KeyboardEvent) => /^(INPUT|TEXTAREA|SELECT)$/u.test((e.target as HTMLElement).tagName);

// The video's time, read every frame while it plays.
function usePlayhead(ref: VideoRef) {
  const [t, setT] = useState(0);
  const [paused, setPaused] = useState(true);
  useEffect(() => {
    const v = ref.current;
    if (!v) return;
    let raf = 0;
    const loop = () => {
      setT(v.currentTime);
      raf = requestAnimationFrame(loop);
    };
    const play = () => {
      setPaused(false);
      cancelAnimationFrame(raf);
      loop();
    };
    const stop = () => {
      setPaused(true);
      cancelAnimationFrame(raf);
      setT(v.currentTime);
    };
    const seeked = () => setT(v.currentTime);
    v.addEventListener("play", play);
    v.addEventListener("pause", stop);
    v.addEventListener("seeked", seeked);
    const hide = () => document.hidden && v.pause();
    document.addEventListener("visibilitychange", hide);
    return () => {
      cancelAnimationFrame(raf);
      v.removeEventListener("play", play);
      v.removeEventListener("pause", stop);
      v.removeEventListener("seeked", seeked);
      document.removeEventListener("visibilitychange", hide);
    };
  }, [ref]);
  return { t, setT, paused };
}

// Space plays and pauses; arrows jump 5 s (shift: 1 s), so stepping back is easy.
function useKeys(ref: VideoRef, seek: (t: number) => void, toggle: () => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const v = ref.current;
      if (!v || isTyping(e) || e.metaKey || e.ctrlKey || e.altKey) return;
      if (e.key === " " && !(e.target as HTMLElement).closest("button")) {
        e.preventDefault();
        toggle();
      }
      const d = { ArrowRight: 1, ArrowLeft: -1 }[e.key];
      if (d) {
        e.preventDefault();
        seek(v.currentTime + d * (e.shiftKey ? 1 : 5));
      }
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [ref, seek, toggle]);
}

// A video the page drives: its time, play state, seek (shown at once, before the frame loads), and the keys.
export function usePlayer(ref: VideoRef, dur: number) {
  const { t, setT, paused } = usePlayhead(ref);
  const seek = useCallback(
    (x: number) => {
      const v = ref.current;
      if (!v) return;
      const to = Math.min(Math.max(0, x), v.duration || dur);
      v.currentTime = to;
      setT(to);
    },
    [ref, dur, setT],
  );
  const toggle = useCallback(() => {
    const v = ref.current;
    if (v?.paused) v.play();
    else v?.pause();
  }, [ref]);
  useKeys(ref, seek, toggle);
  return { t, paused, seek, toggle };
}

// Esc goes to href, unless a text field or something else already took the key.
export function useEscapeTo(href: string | undefined) {
  useEffect(() => {
    if (href === undefined) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented || isTyping(e)) return;
      location.hash = href;
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [href]);
}
