import { useEffect, useState, type RefObject } from "react";
import { mmssd } from "./lib";

// The video's time, read every frame while it plays.
export function usePlayhead(ref: RefObject<HTMLVideoElement | null>) {
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
export function useKeys(ref: RefObject<HTMLVideoElement | null>, seek: (t: number) => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement;
      const v = ref.current;
      if (!v || /^(INPUT|TEXTAREA|SELECT)$/u.test(el.tagName) || e.metaKey || e.ctrlKey || e.altKey) return;
      if (e.key === " " && !el.closest("button")) {
        e.preventDefault();
        if (v.paused) v.play();
        else v.pause();
      }
      const d = { ArrowRight: 1, ArrowLeft: -1 }[e.key];
      if (d) {
        e.preventDefault();
        seek(v.currentTime + d * (e.shiftKey ? 1 : 5));
      }
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [ref, seek]);
}

export function PlayButton({ paused, onClick }: { paused: boolean; onClick: () => void }) {
  return (
    <button className="btn pbtn" onClick={onClick} aria-label={paused ? "Play" : "Pause"}>
      <svg viewBox="0 0 14 14" aria-hidden="true">
        {paused ? <path d="M3 1.5v11l9.5-5.5z" /> : <path d="M3 1.5h3v11H3zM8 1.5h3v11H8z" />}
      </svg>
    </button>
  );
}

export type Mark = { at: number; kind: "comment" | "chg" | "cut"; key: string };

// A scrub bar: drag anywhere, and the playhead follows at once without waiting for the seek.
export function Scrub({
  t,
  dur,
  marks,
  onSeek,
}: {
  t: number;
  dur: number;
  marks: Mark[];
  onSeek: (t: number) => void;
}) {
  const pc = (x: number) => `${(Math.min(Math.max(x, 0), dur) / dur) * 100}%`;
  const at = (e: React.PointerEvent<HTMLDivElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    onSeek(((e.clientX - r.left) / r.width) * dur);
  };
  return (
    <div
      className="track"
      // oxlint-disable-next-line jsx-a11y/prefer-tag-over-role -- a range input can't draw the change and comment marks on its track
      role="slider"
      tabIndex={0}
      aria-label="Playhead"
      aria-valuemin={0}
      aria-valuemax={dur}
      aria-valuenow={t}
      aria-valuetext={mmssd(t)}
      onPointerDown={(e) => {
        e.currentTarget.setPointerCapture(e.pointerId);
        at(e);
      }}
      onPointerMove={(e) => e.buttons && at(e)}
    >
      <i className="rail" />
      <i className="fill" style={{ width: pc(t) }} />
      {marks.map((m) =>
        m.kind === "comment" ? (
          <i key={m.key} className="dot" style={{ left: pc(m.at) }} />
        ) : (
          <i key={m.key} className={`tick ${m.kind}`} style={{ left: pc(m.at) }} />
        ),
      )}
      <i className="ph" style={{ left: pc(t) }} />
    </div>
  );
}
