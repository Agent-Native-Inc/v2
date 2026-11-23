import { useCallback, useEffect, useRef, useState } from "react";
import { mmssd, type Comment, type Version } from "./lib";
import { PlayButton, Scrub, useKeys, usePlayhead, type Mark } from "./Player";
import { Box, Top } from "./Feed";

// points as fractions of the frame, so they survive resizing
type Stroke = [number, number][];

const point = (e: React.PointerEvent<HTMLCanvasElement>): [number, number] => {
  const r = e.currentTarget.getBoundingClientRect();
  return [(e.clientX - r.left) / r.width, (e.clientY - r.top) / r.height];
};

// One version, Flask-style: the video, a box under it for timed comments (with a drawing on the
// frame), and the prompt that made the version above its comments.
export function CommentMode({
  project,
  V,
  comments,
  refresh,
}: {
  project: string;
  V: Version;
  comments: Comment[];
  refresh: () => Promise<void>;
}) {
  const ver = V.id;
  const video = useRef<HTMLVideoElement>(null);
  const { t, setT, paused } = usePlayhead(video);
  const [dur, setDur] = useState(V.dur);
  const seek = useCallback(
    (x: number) => {
      const v = video.current;
      if (!v) return;
      const to = Math.min(Math.max(0, x), v.duration || dur);
      v.currentTime = to;
      setT(to);
    },
    [dur, setT],
  );
  useKeys(video, seek);

  const [text, setText] = useState("");
  const [drawing, setDrawing] = useState(false);
  const [strokes, setStrokes] = useState<Stroke[]>([]);
  const [selected, setSelected] = useState<string>();
  const [showDone, setShowDone] = useState(false);
  const [error, setError] = useState("");

  const mine = comments.filter((c) => c.version === ver).toSorted((a, b) => a.at - b.at);
  const open = mine.filter((c) => !c.done_in);
  const shown = showDone ? mine : open;
  const sel = mine.find((c) => c.id === selected);

  const marks: Mark[] = [
    ...V.changes.map((c, i): Mark => ({ at: c.from, kind: "chg", key: `s${i}` })),
    ...V.cuts.map((c, i): Mark => ({ at: c.at, kind: "cut", key: `c${i}` })),
    ...open.map((c): Mark => ({ at: c.at, kind: "comment", key: c.id })),
  ];

  // ---- drawing on the frame ----
  const canvas = useRef<HTMLCanvasElement>(null);
  const live = useRef<Stroke | null>(null);
  const paint = useCallback(() => {
    const c = canvas.current;
    if (!c) return;
    const dpr = devicePixelRatio || 1;
    c.width = c.clientWidth * dpr;
    c.height = c.clientHeight * dpr;
    const ctx = c.getContext("2d");
    if (!ctx) return;
    drawStrokes(ctx, live.current ? [...strokes, live.current] : strokes, c.width, c.height);
  }, [strokes]);
  useEffect(() => {
    paint();
    const c = canvas.current;
    if (!c) return;
    const ro = new ResizeObserver(paint);
    ro.observe(c);
    return () => ro.disconnect();
  }, [paint]);
  const startDrawing = () => {
    video.current?.pause();
    setDrawing(true);
    setSelected(undefined);
  };

  // ---- sending ----
  const send = async (e: React.FormEvent) => {
    e.preventDefault();
    const v = video.current;
    if (!v || !text.trim()) return;
    const at = v.currentTime;
    const form = new FormData();
    form.set("version", ver);
    form.set("at", String(Math.round(at * 10) / 10));
    form.set("text", text.trim());
    if (strokes.length > 0) form.set("drawing", await frameWithStrokes(v, strokes), "drawing.png");
    const r = await fetch("/api/comments", { method: "POST", body: form });
    if (!r.ok) return setError(await r.text());
    const c: Comment = await r.json();
    setText("");
    setStrokes([]);
    setDrawing(false);
    setError("");
    await refresh();
    setSelected(c.id);
  };
  const act = async (id: string, action: "done" | "reopen" | "delete") => {
    const r = await fetch(`/api/comments/${id}/${action}`, { method: "POST" });
    if (!r.ok) return setError(await r.text());
    await refresh();
  };
  const pick = (c: Comment) => {
    video.current?.pause();
    seek(c.at);
    setSelected(c.id);
    setDrawing(false);
  };

  // A comment's drawing shows over the video while paused on its moment.
  const overlay =
    sel?.drawing && paused && Math.abs(t - sel.at) < 0.15 ? { src: `/comments/${sel.drawing}`, id: sel.id } : undefined;

  return (
    <>
      <Top crumbs={[project, ver]} back={{ href: "#", label: "All versions" }}>
        <span className="muted small">Then tell Claude “fix my comments”</span>
      </Top>
      <div className="cm">
        <div className="stage">
          <div className="big" style={{ aspectRatio: V.ar || 16 / 9 }}>
            {/* oxlint-disable-next-line jsx-a11y/media-has-caption -- user renders have no caption track */}
            <video
              ref={video}
              src={`/video/${ver}`}
              playsInline
              preload="auto"
              onLoadedMetadata={(e) => setDur(e.currentTarget.duration)}
              onClick={(e) => !drawing && (e.currentTarget.paused ? e.currentTarget.play() : e.currentTarget.pause())}
            />
            {overlay && <img className="drawn" src={overlay.src} alt={`Drawing for ${overlay.id}`} />}
            <canvas
              ref={canvas}
              className={drawing ? "ink on" : "ink"}
              onPointerDown={(e) => {
                if (!drawing) return;
                e.currentTarget.setPointerCapture(e.pointerId);
                live.current = [point(e)];
                paint();
              }}
              onPointerMove={(e) => {
                if (!live.current) return;
                live.current.push(point(e));
                paint();
              }}
              onPointerUp={() => {
                const done = live.current;
                live.current = null;
                if (done) setStrokes((s) => [...s, done]);
              }}
            />
          </div>

          <form className="composer" onSubmit={send}>
            <button
              type="button"
              className={`tool${drawing ? " on" : ""}`}
              onClick={() => (drawing ? setDrawing(false) : startDrawing())}
              title="Draw on the frame"
              aria-label="Draw on the frame"
              aria-pressed={drawing}
            >
              <svg viewBox="0 0 24 24" aria-hidden="true">
                <path d="M4 20l4-1 11-11-3-3L5 16z" />
              </svg>
            </button>
            <input
              value={text}
              onChange={(e) => setText(e.target.value)}
              placeholder={`Comment at ${mmssd(t)}`}
              aria-label="Comment at the current time"
            />
            {strokes.length > 0 && (
              <button type="button" className="tag" onClick={() => setStrokes([])}>
                drawing ×
              </button>
            )}
            <button className="send" disabled={!text.trim()} aria-label="Add comment">
              →
            </button>
          </form>
          {error && <p className="err">{error}</p>}

          <div className="scrub">
            <PlayButton paused={paused} onClick={() => (paused ? video.current?.play() : video.current?.pause())} />
            <button className="btn skip mono" onClick={() => seek(t - 5)} aria-label="Back 5 seconds">
              −5s
            </button>
            <span className="mono time">
              {mmssd(t)} <span className="muted">/ {mmssd(dur || 0)}</span>
            </span>
            {dur > 0 && <Scrub t={t} dur={dur} marks={marks} onSeek={seek} />}
          </div>
        </div>

        <aside className="side">
          <Box title={`Prompt${V.prompts.length > 1 ? "s" : ""} that made ${ver}`}>
            <div className="box-body">
              {(V.prompts.length > 0 ? V.prompts : [V.prompt]).map((p, i) => (
                <p key={i} className="first-prompt">
                  {p}
                </p>
              ))}
            </div>
          </Box>
          <Box
            title={<>Comments{open.length > 0 ? <span className="count">{open.length}</span> : null}</>}
            action={
              mine.length > open.length && (
                <button className="btn text" onClick={() => setShowDone(!showDone)}>
                  {showDone ? "Hide done" : `Show ${mine.length - open.length} done`}
                </button>
              )
            }
          >
            {shown.length > 0 ? (
              <ul className="c-list">
                {shown.map((c) => (
                  <li key={c.id} className={`c${c.id === selected ? " on" : ""}${c.done_in ? " done" : ""}`}>
                    <button
                      type="button"
                      className="c-pick"
                      onClick={() => pick(c)}
                      aria-label={`Go to ${c.id} at ${mmssd(c.at)}`}
                    />
                    <div className="c-who">
                      <b>{c.id}</b>
                      <span className="stamp">{mmssd(c.at)}</span>
                    </div>
                    <p className="c-text">{c.text}</p>
                    {c.drawing && <img className="thumb" src={`/comments/${c.drawing}`} alt="Drawing on the frame" />}
                    <div className="acts">
                      {c.done_in ? (
                        <>
                          <span className="muted">{c.done_in === "you" ? "Done by you" : `Done in ${c.done_in}`}</span>
                          <button className="btn text" onClick={() => act(c.id, "reopen")}>
                            Reopen
                          </button>
                        </>
                      ) : (
                        <button className="btn text" onClick={() => act(c.id, "done")}>
                          Done
                        </button>
                      )}
                      <button className="btn text" onClick={() => act(c.id, "delete")} aria-label={`Delete ${c.id}`}>
                        Delete
                      </button>
                    </div>
                  </li>
                ))}
              </ul>
            ) : (
              <div className="box-body">
                <p className="c-empty muted">
                  Pause where you want something different and write what you want. Draw on the frame to show where.
                </p>
              </div>
            )}
          </Box>
          {mine.some((c) => c.done_in) && <p className="muted">Done comments are cleared on the next save.</p>}
        </aside>
      </div>
    </>
  );
}

function drawStrokes(ctx: CanvasRenderingContext2D, strokes: Stroke[], w: number, h: number) {
  ctx.clearRect(0, 0, w, h);
  ctx.strokeStyle = "#fff";
  ctx.lineWidth = Math.max(3, w / 220);
  ctx.lineCap = ctx.lineJoin = "round";
  ctx.shadowColor = "rgba(0,0,0,.6)";
  ctx.shadowBlur = ctx.lineWidth;
  for (const s of strokes) {
    ctx.beginPath();
    s.forEach(([x, y], i) => (i ? ctx.lineTo(x * w, y * h) : ctx.moveTo(x * w, y * h)));
    const [first] = s;
    if (s.length === 1 && first) ctx.lineTo(first[0] * w + 0.1, first[1] * h);
    ctx.stroke();
  }
}

// The frame as the user sees it, with their marks on it, at the video's full size: what the agent looks at.
function frameWithStrokes(v: HTMLVideoElement, strokes: Stroke[]): Promise<Blob> {
  const c = document.createElement("canvas");
  c.width = v.videoWidth;
  c.height = v.videoHeight;
  const ctx = c.getContext("2d");
  if (!ctx) throw new Error("This browser can't draw on a canvas");
  drawStrokes(ctx, strokes, c.width, c.height);
  ctx.globalCompositeOperation = "destination-over";
  ctx.shadowBlur = 0;
  ctx.drawImage(v, 0, 0, c.width, c.height);
  return new Promise((ok, fail) => {
    c.toBlob((b) => (b ? ok(b) : fail(new Error("Couldn't read the frame"))), "image/png");
  });
}
