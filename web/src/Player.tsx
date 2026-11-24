import { mmssd } from "./lib";

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
