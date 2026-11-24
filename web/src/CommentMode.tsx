import { useRef, useState } from "react";
import { api, mmssd, type Comment, type Version } from "./lib";
import { usePlayer } from "./hooks";
import { PlayButton, Scrub, type Mark } from "./Player";
import { Box, Top } from "./Chrome";
import { Ink, frameWithStrokes, type Stroke } from "./Ink";

// One version: the video, a box under it for timed comments (with a drawing on the
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
  const [dur, setDur] = useState(V.dur);
  const { t, paused, seek, toggle } = usePlayer(video, dur);

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

  const startDrawing = () => {
    video.current?.pause();
    setDrawing(true);
    setSelected(undefined);
  };

  const send = async (e: React.FormEvent) => {
    e.preventDefault();
    const v = video.current;
    if (!v || !text.trim()) return;
    const at = v.currentTime;
    const form = new FormData();
    form.set("version", ver);
    form.set("at", String(Math.round(at * 10) / 10));
    form.set("text", text.trim());
    try {
      if (strokes.length > 0) form.set("drawing", await frameWithStrokes(v, strokes), "drawing.png");
      const c = await api<Comment>("/api/comments", { method: "POST", body: form });
      setText("");
      setStrokes([]);
      setDrawing(false);
      setError("");
      await refresh();
      setSelected(c.id);
    } catch (err) {
      setError((err as Error).message);
    }
  };
  const act = async (id: string, action: "done" | "reopen" | "delete") => {
    try {
      await api(`/api/comments/${id}/${action}`, { method: "POST" });
      await refresh();
    } catch (err) {
      setError((err as Error).message);
    }
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
        <span className="muted small">{'Then tell your agent "fix my comments"'}</span>
      </Top>
      <div className="cm">
        <div className="stage">
          <div className="big" style={{ aspectRatio: V.ar }}>
            {/* oxlint-disable-next-line jsx-a11y/media-has-caption -- user renders have no caption track */}
            <video
              ref={video}
              src={`/video/${ver}`}
              playsInline
              preload="auto"
              onLoadedMetadata={(e) => setDur(e.currentTarget.duration)}
              onClick={() => !drawing && toggle()}
            />
            {overlay && <img className="drawn" src={overlay.src} alt={`Drawing for ${overlay.id}`} />}
            <Ink on={drawing} strokes={strokes} onStroke={(st) => setStrokes((s) => [...s, st])} />
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
            <PlayButton paused={paused} onClick={toggle} />
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
