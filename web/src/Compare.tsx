import { useCallback, useEffect, useRef, useState } from "react";
import { History, mmss, mmssd, secs, type Version } from "./lib";
import { PlayButton, Scrub, useKeys, usePlayhead } from "./Player";
import { Box, Top } from "./Feed";

// ver beside an earlier version, played in step: the earlier one follows across every cut in between.
export function Compare({ project, h, A, B }: { project: string; h: History; A: Version; B: Version }) {
  const ver = A.id;
  const base = B.id;
  const a = useRef<HTMLVideoElement>(null);
  const b = useRef<HTMLVideoElement>(null);
  const { t, setT, paused } = usePlayhead(a);
  const [D, setD] = useState(A.dur);
  const items = h.changesSince(ver, base);
  const cuts = items.filter((x) => x.cut && !x.gone);
  const cutTotal = cuts.reduce((s, c) => s + (c.removed || 0), 0);

  // The earlier version is only re-seeked when it's really off (after a seek, or a jump over a cut).
  const sync = useCallback(
    (force: boolean) => {
      const v = b.current;
      if (!a.current || !v) return;
      const want = h.back(ver, base, a.current.currentTime);
      if (force || Math.abs(v.currentTime - want) > 0.3) v.currentTime = Math.min(want, v.duration || want);
    },
    [h, ver, base],
  );
  useEffect(() => {
    if (!paused) sync(false);
    // oxlint-disable-next-line react/exhaustive-effect-dependencies -- t re-runs the sync on every playhead tick
  }, [t, paused, sync]);
  useEffect(() => {
    const v = a.current;
    if (!v) return;
    const play = () => {
      sync(true);
      b.current?.play();
    };
    const pause = () => {
      b.current?.pause();
      sync(true);
    };
    const seeked = () => sync(true);
    v.addEventListener("play", play);
    v.addEventListener("pause", pause);
    v.addEventListener("seeked", seeked);
    return () => {
      v.removeEventListener("play", play);
      v.removeEventListener("pause", pause);
      v.removeEventListener("seeked", seeked);
    };
  }, [sync]);

  const seek = useCallback(
    (x: number) => {
      const v = a.current;
      if (!v) return;
      const to = Math.min(Math.max(0, x), v.duration || D);
      v.currentTime = to;
      setT(to);
    },
    [D, setT],
  );
  useKeys(a, seek);
  const skipped = cuts.find((c) => t >= c.from && t < c.from + 1.6);

  return (
    <>
      <Top crumbs={[project, `${ver} vs ${base}`]} back={{ href: "#", label: "All versions" }}>
        <a className="btn" href={`#${ver}`}>
          Stop comparing
        </a>
      </Top>
      <main className="wrap">
        <h2 className="title">{A.prompt}</h2>
        <p className="muted small">
          {ver} · saved {A.saved}
          {D ? ` · ${mmss(D)}` : ""} ·{" "}
          <b>
            {items.filter((x) => !x.gone).length} changes since {base}
          </b>
          {cutTotal > 0 && (
            <>
              {" "}
              · <b>{secs(cutTotal)} shorter</b>
            </>
          )}
        </p>
        <div className="pair" style={{ gridTemplateColumns: `${B.ar || 0.5625}fr ${A.ar || 0.5625}fr` }}>
          <figure>
            <figcaption>
              <b className="mono">{base}</b> {B.saved}
            </figcaption>
            <div className="screen" style={{ aspectRatio: B.ar || 9 / 16 }}>
              <video ref={b} src={`/video/${base}`} playsInline preload="auto" muted />
              {skipped && (
                <span className="skipped tag">
                  Skipped {secs(skipped.removed ?? 0)} that {skipped.by} cut
                </span>
              )}
            </div>
          </figure>
          <figure>
            <figcaption>
              <b className="mono">{ver}</b> {A.saved}
            </figcaption>
            <div className="screen" style={{ aspectRatio: A.ar || 9 / 16 }}>
              {/* oxlint-disable-next-line jsx-a11y/media-has-caption -- user renders have no caption track */}
              <video
                ref={a}
                src={`/video/${ver}`}
                playsInline
                preload="auto"
                onLoadedMetadata={(e) => setD(A.dur || e.currentTarget.duration)}
              />
            </div>
          </figure>
        </div>
        <div className="scrub">
          <PlayButton paused={paused} onClick={() => (paused ? a.current?.play() : a.current?.pause())} />
          <span className="mono time">
            {mmssd(t)} <span className="muted">/ {mmssd(D)}</span>
          </span>
          {D > 0 && (
            <Scrub
              t={t}
              dur={D}
              onSeek={seek}
              marks={items
                .filter((x) => !x.whole && !x.gone)
                .map((x) =>
                  x.cut
                    ? { at: x.from, kind: "cut" as const, key: x.k }
                    : { at: x.from, kind: "chg" as const, key: x.k },
                )}
            />
          )}
        </div>
        <Box title={`Changes from ${base} to ${ver}`}>
          <ol className="lanes">
            {items.map((x) => {
              const on = !x.whole && !x.gone && !x.cut && t >= x.from && t < x.to;
              const when = x.gone
                ? ""
                : x.whole
                  ? "whole video"
                  : x.cut
                    ? mmssd(x.from)
                    : `${mmssd(x.from)}–${mmssd(x.to)}`;
              const body = (
                <>
                  <span className={x.gone || x.whole ? "muted" : "stamp"}>{when}</span>
                  <span>
                    {x.text}
                    {x.cut && ` · ${secs(x.removed ?? 0)}`}
                    {x.gone && <span className="muted"> — cut later</span>}
                  </span>
                  <span className="mono muted">{x.by}</span>
                </>
              );
              return (
                <li key={x.k}>
                  {x.whole || x.gone ? (
                    <div className="lane">{body}</div>
                  ) : (
                    <button
                      className={`lane${on ? " on" : ""}`}
                      onClick={() => {
                        seek(x.cut ? Math.max(0, x.from - 1.5) : x.from + 0.01);
                        a.current?.play();
                      }}
                    >
                      {body}
                    </button>
                  )}
                </li>
              );
            })}
          </ol>
        </Box>
      </main>
    </>
  );
}
