import { useEffect, useRef, useState, type RefObject } from "react";
import { mmss, mmssd, secs, type ChangeKind, type Span, type Version, type Whole } from "./lib";

// What a version changed, laid out like an editor: a lane per kind (text, graphics, voice...),
// a block per change showing what it is, all following the version's video above it.

type Lane = ChangeKind | "other";
const LANES: { id: Lane; label: string }[] = [
  { id: "text", label: "Text" },
  { id: "captions", label: "Captions" },
  { id: "graphics", label: "Graphics" },
  { id: "video", label: "Video" },
  { id: "voice", label: "Voice" },
  { id: "music", label: "Music" },
  { id: "sfx", label: "SFX" },
  { id: "other", label: "Changes" },
];
const laneOf = (c: Span): Lane => c.kind ?? "other";
const sound = (l: Lane) => l === "voice" || l === "music" || l === "sfx";
const fileName = (src: string) => src.slice(src.lastIndexOf("/") + 1);
// The words on screen, when the agent quoted them ('Faster iteration').
const quoted = (text: string) => [...text.matchAll(/'([^']{2,60})'/gu)].map((m) => m[1]).at(-1);

function KindIcon({ kind }: { kind: Lane | "all" }) {
  if (kind === "text") return <b className="tl-glyph">T</b>;
  if (kind === "captions") return <b className="tl-glyph">CC</b>;
  const d: Partial<Record<Lane | "all", React.ReactNode>> = {
    graphics: (
      <>
        <rect x="2" y="2" width="4.5" height="4.5" rx="1" />
        <circle cx="9.5" cy="9.5" r="2.5" />
      </>
    ),
    video: (
      <>
        <rect x="1.5" y="3" width="8" height="8" rx="1.5" />
        <path d="M9.5 6l3-2v6l-3-2" />
      </>
    ),
    voice: <path d="M2 6.5v1M4.5 4.5v5M7 2.5v9M9.5 4.5v5M12 6v2" />,
    music: (
      <>
        <path d="M5 11V3l7-1.5v8" />
        <circle cx="3.5" cy="11" r="1.5" />
        <circle cx="10.5" cy="9.5" r="1.5" />
      </>
    ),
    sfx: <path d="M2 7h2l2-4 2 8 2-4h2" />,
    other: <path d="M3 7h8M7 3v8" />,
    all: <path d="M2 7h10" />,
  };
  return (
    <svg className="tl-ic" viewBox="0 0 14 14" aria-hidden="true">
      {d[kind]}
    </svg>
  );
}

// Overlapping changes of one kind stack into the next track, like an editor.
function pack(cs: Span[]) {
  const rows: { end: number; spans: Span[] }[] = [];
  for (const c of cs.toSorted((a, b) => a.from - b.from)) {
    const row = rows.find((r) => r.end <= c.from + 0.01);
    if (row) {
      row.spans.push(c);
      row.end = c.to;
    } else rows.push({ end: c.to, spans: [c] });
  }
  return rows.map((r) => r.spans);
}

// The render's loudness, 100 values a second, from the server; loaded once per version.
// A failed load isn't kept, so the next look tries again.
const loudness = new Map<string, Promise<Float32Array>>();
function loadLoudness(v: string) {
  let load = loudness.get(v);
  if (!load) {
    load = fetch(`/peaks/${v}`).then(async (r) => {
      if (!r.ok) throw new Error(await r.text());
      return new Float32Array(await r.arrayBuffer());
    });
    load.catch(() => loudness.delete(v));
    loudness.set(v, load);
  }
  return load;
}
function useLoudness(v: string, on: boolean) {
  const [peaks, setPeaks] = useState<Float32Array>();
  useEffect(() => {
    if (on) loadLoudness(v).then(setPeaks, () => setPeaks(undefined));
  }, [v, on]);
  return peaks;
}

function Wave({ peaks, from, to }: { peaks: Float32Array | undefined; from: number; to: number }) {
  if (!peaks?.length) return null;
  const a = Math.floor(from * 100);
  const b = Math.max(a + 1, Math.ceil(to * 100));
  const n = Math.min(120, b - a);
  const step = (b - a) / n;
  const top = Math.max(1e-3, ...peaks.subarray(a, b));
  const bars = Array.from({ length: n }, (_, i) => {
    let m = 0;
    for (let j = Math.floor(a + i * step); j < Math.floor(a + (i + 1) * step); j++) m = Math.max(m, peaks[j] || 0);
    return Math.sqrt(m / top);
  });
  return (
    <svg className="tl-wave" viewBox={`0 0 ${n} 2`} preserveAspectRatio="none" aria-hidden="true">
      {bars.map((p, i) => (
        <rect key={i} x={i + 0.2} width={0.6} y={1 - p * 0.9} height={Math.max(0.06, p * 1.8)} />
      ))}
    </svg>
  );
}

function Inside({ c, lane, peaks }: { c: Span; lane: Lane; peaks: Float32Array | undefined }) {
  if (lane === "text" || lane === "captions")
    return (
      <>
        <span className="tl-small">
          <KindIcon kind={lane} />
        </span>
        <span className="tl-words">{lane === "text" ? (quoted(c.text) ?? c.text) : c.text}</span>
      </>
    );
  if (sound(lane))
    return (
      <>
        <span className="tl-small">
          <KindIcon kind={lane} />
        </span>
        {c.src && <span className="tl-file">{fileName(c.src)}</span>}
        <Wave peaks={peaks} from={c.from} to={c.to} />
      </>
    );
  return (
    <>
      <KindIcon kind={lane} />
      <span className="tl-words tl-label">{c.text.split(/[,:]/u)[0]}</span>
    </>
  );
}

type Picked = Span | Whole;

export function Timeline({ v, video }: { v: Version; video: RefObject<HTMLVideoElement | null> }) {
  const root = useRef<HTMLDivElement>(null);
  const ruler = useRef<HTMLDivElement>(null);
  const [t, setT] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [pick, setPick] = useState<Picked>();
  const dur = v.dur;
  const peaks = useLoudness(
    v.id,
    v.changes.some((c) => sound(laneOf(c))),
  );

  // The playhead moves through a CSS variable every frame; React re-renders only when what's on screen changes.
  useEffect(() => {
    const el = video.current;
    if (!el || !dur) return;
    let raf = 0;
    let key = "";
    const tick = () => {
      const now = el.currentTime;
      root.current?.style.setProperty("--p", String(now / dur));
      const k = v.changes.map((c) => +(now >= c.from && now < c.to)).join("");
      if (k !== key) {
        key = k;
        setT(now);
      }
    };
    const loop = () => {
      tick();
      raf = requestAnimationFrame(loop);
    };
    const play = () => {
      setPlaying(true);
      cancelAnimationFrame(raf);
      loop();
    };
    const stop = () => {
      setPlaying(false);
      cancelAnimationFrame(raf);
      tick();
    };
    // A seek while playing must not stop the loop; it only needs the new position drawn.
    el.addEventListener("play", play);
    el.addEventListener("pause", stop);
    el.addEventListener("seeked", tick);
    return () => {
      cancelAnimationFrame(raf);
      el.removeEventListener("play", play);
      el.removeEventListener("pause", stop);
      el.removeEventListener("seeked", tick);
    };
  }, [video, v, dur]);

  if (!dur) return null;
  const pc = (x: number) => `${(x / dur) * 100}%`;
  const seek = (x: number, play: boolean) => {
    const el = video.current;
    if (!el) return;
    // oxlint-disable-next-line react/immutability -- seeking writes to the video element, not to React state
    el.currentTime = Math.min(Math.max(0, x), dur);
    root.current?.style.setProperty("--p", String(el.currentTime / dur));
    // don't wait for the video to load the frame
    setT(el.currentTime);
    if (play) el.play();
  };
  const at = (e: React.PointerEvent) => {
    const r = ruler.current?.getBoundingClientRect();
    if (!r) return;
    seek(((e.clientX - r.left) / r.width) * dur, false);
  };
  const live = v.changes.filter((c) => t >= c.from && t < c.to);
  // While playing, the words follow the video; otherwise they follow what you point at.
  const shown: Picked[] = playing || !pick ? live : [pick];
  const step = dur > 120 ? 30 : dur > 40 ? 10 : 5;

  return (
    <div className="tl" ref={root} style={{ "--p": 0 } as React.CSSProperties} onMouseLeave={() => setPick(undefined)}>
      <div
        className="tl-scrub"
        onPointerDown={(e) => {
          if ((e.target as HTMLElement).closest(".tl-el, .tl-all")) return;
          e.currentTarget.setPointerCapture(e.pointerId);
          at(e);
        }}
        onPointerMove={(e) => e.buttons && e.currentTarget.hasPointerCapture(e.pointerId) && at(e)}
      >
        <div className="tl-ruler">
          <span />
          <div ref={ruler}>
            {Array.from({ length: Math.floor(dur / step) + 1 }, (_, i) => i * step).map((x) => (
              <i key={x} style={{ left: pc(x) }}>
                {x % (step * 2) ? "" : mmss(x)}
              </i>
            ))}
            {v.cuts.map((c, i) => (
              <i
                key={`c${i}`}
                className="tl-cut"
                style={{ left: pc(c.at) }}
                title={`Cut ${secs(c.removed)}: ${c.text}`}
              />
            ))}
            <b className="tl-knob" />
          </div>
        </div>
        <div className="tl-lanes">
          {v.whole.length > 0 && (
            <div className="tl-lane">
              <span className="tl-name">All</span>
              <div className="tl-track tl-alltrack">
                {v.whole.map((c, i) => (
                  <button
                    key={i}
                    className={`tl-all${pick === c ? " picked" : ""}`}
                    onClick={() => setPick(c)}
                    onMouseEnter={() => setPick(c)}
                    onFocus={() => setPick(c)}
                    aria-label={`Whole video: ${c.text}${c.src ? `, from ${c.src}` : ""}`}
                  >
                    <KindIcon kind={c.kind ?? "all"} />
                    <span>{c.text}</span>
                  </button>
                ))}
              </div>
            </div>
          )}
          {LANES.flatMap((l) =>
            pack(v.changes.filter((c) => laneOf(c) === l.id)).map((row, ri) => (
              <div className="tl-lane" key={l.id + ri}>
                <span className="tl-name">
                  <span className={`tl-dot k-${l.id}`}>
                    <KindIcon kind={l.id} />
                  </span>
                  {l.label}
                  {ri ? ` ${ri + 1}` : ""}
                </span>
                <div className="tl-track">
                  {row.map((c, i) => (
                    <button
                      key={i}
                      className={`tl-el k-${l.id}${t >= c.from && t < c.to ? " on" : ""}${pick === c ? " picked" : ""}`}
                      style={{ left: pc(c.from), width: `max(20px, calc(${pc(c.to - c.from)} - 2px))` }}
                      onClick={() => {
                        setPick(c);
                        seek(c.from + 0.01, true);
                      }}
                      onMouseEnter={() => setPick(c)}
                      onFocus={() => setPick(c)}
                      aria-label={`${mmssd(c.from)} to ${mmssd(c.to)}: ${c.text}${c.src ? `, from ${c.src}` : ""}`}
                    >
                      <Inside c={c} lane={l.id} peaks={peaks} />
                    </button>
                  ))}
                </div>
              </div>
            )),
          )}
          <i className="tl-ph" />
        </div>
      </div>
      <div className="tl-detail" aria-live="polite">
        {shown.length === 0 && <span className="tl-empty">Play, scrub, or point at a change</span>}
        {shown.map((c, i) => {
          const lane = "from" in c ? laneOf(c) : undefined;
          return (
            <div className="tl-line" key={i}>
              <span className={`tl-dot k-${lane ?? "all"}`}>
                <KindIcon kind={lane ?? c.kind ?? "all"} />
              </span>
              <span className="tl-text">{c.text}</span>
              {c.src && <span className="tl-src mono">{c.src}</span>}
              <span className="tl-when mono">{"from" in c ? `${mmssd(c.from)}–${mmssd(c.to)}` : "whole video"}</span>
            </div>
          );
        })}
      </div>
    </div>
  );
}
