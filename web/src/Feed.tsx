import { useRef } from "react";
import { History, mmss, restoredFrom, type Comment, type Version } from "./lib";
import { Top } from "./Chrome";
import { Timeline } from "./Timeline";

// Every version, newest first, with what changed between each one and the one before it.
export function Feed({ project, h, comments }: { project: string; h: History; comments: Comment[] }) {
  const latest = h.versions.at(-1);
  if (!latest) return null;
  return (
    <>
      <Top crumbs={[project]}>
        <span className="muted small">
          {h.versions.length} versions · latest {latest.id}
          {latest.dur ? ` · ${mmss(latest.dur)}` : ""}
        </span>
      </Top>
      <main className="wrap feed">
        {h.versions.toReversed().map((v) => (
          <Entry
            key={v.id}
            v={v}
            h={h}
            latest={v === latest}
            open={comments.filter((c) => c.version === v.id && !c.done_in).length}
          />
        ))}
      </main>
    </>
  );
}

// One version: its video, what it was, and what it changed, following the video.
function Entry({ v, h, latest, open }: { v: Version; h: History; latest: boolean; open: number }) {
  const video = useRef<HTMLVideoElement>(null);
  const from = restoredFrom(v);
  const earlier = h.versions
    .slice(0, h.num(v.id))
    .toReversed()
    .filter((x) => x.video);
  const lines = [
    ...v.changes.map((c) => c.text),
    ...v.cuts.map((c) => `cut: ${c.text}`),
    ...v.whole.map((w) => w.text),
  ];
  return (
    <section className="entry">
      <div className="card">
        <div className="vid" style={{ aspectRatio: v.ar, width: `min(100%, calc(52dvh * ${v.ar}))` }}>
          {v.video ? (
            // oxlint-disable-next-line jsx-a11y/media-has-caption -- user renders have no caption track
            <video ref={video} src={`/video/${v.id}`} poster={`/poster/${v.id}`} controls playsInline preload="none" />
          ) : (
            <span className="novid">No render saved</span>
          )}
        </div>
        <div>
          <div className="vtag">
            {v.id}
            {latest && <em>Latest</em>}
            {v.dur ? <span className="muted">{mmss(v.dur)}</span> : null}
          </div>
          <h2 className="title">{from ? `Restored ${from}` : v.prompt}</h2>
          <div className="muted small saved">saved {v.saved}</div>
          <div className="row">
            {v.video && (
              <a className={`btn${open || latest ? " primary" : ""}`} href={`#${v.id}`}>
                Comment mode{open ? ` · ${open} open` : ""}
              </a>
            )}
            {v.video && earlier.length > 0 && (
              <details className="menu">
                <summary className="btn">Compare with…</summary>
                <div className="pop">
                  {earlier.map((x) => (
                    <a key={x.id} href={`#${v.id}/vs/${x.id}`}>
                      <b className="mono">{x.id}</b>
                      <span>{x.prompt}</span>
                    </a>
                  ))}
                </div>
              </details>
            )}
          </div>
        </div>
      </div>
      {v.video && v.dur ? (
        <Timeline v={v} video={video} />
      ) : (
        lines.length > 0 && (
          <ul className="plain">
            {lines.map((l, i) => (
              <li key={i}>{l}</li>
            ))}
          </ul>
        )
      )}
    </section>
  );
}
