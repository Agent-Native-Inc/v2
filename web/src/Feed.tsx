import { Fragment, useEffect, useRef } from "react";
import { History, mmss, restoredFrom, type Comment, type Version } from "./lib";
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

// back: where the arrow (and Esc) goes, e.g. "#" for every version. Omitted on the home page.
export function Top({
  crumbs,
  back,
  children,
}: {
  crumbs: string[];
  back?: { href: string; label: string };
  children?: React.ReactNode;
}) {
  const backHref = back?.href;
  useEffect(() => {
    if (backHref === undefined) return;
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement;
      if (e.key !== "Escape" || e.defaultPrevented || /^(INPUT|TEXTAREA|SELECT)$/u.test(el.tagName)) return;
      location.hash = backHref;
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [backHref]);
  return (
    <header className="top">
      {back && (
        <a className="btn back" href={back.href} aria-label={`Back to ${back.label}`}>
          <svg viewBox="0 0 16 16" aria-hidden="true">
            <path d="M10 3L5 8l5 5" />
          </svg>
          <span>{back.label}</span>
        </a>
      )}
      <nav className="crumbs" aria-label="Breadcrumb">
        {/* oxlint-disable-next-line jsx-a11y/anchor-is-valid -- "#" is the home route of this hash router */}
        <a href="#" className="brand">
          <Logo />
          v2
        </a>
        {crumbs.map((c, i) => (
          <Fragment key={c}>
            <span className="sep">/</span>
            {i < crumbs.length - 1 ? (
              // oxlint-disable-next-line jsx-a11y/anchor-is-valid -- "#" is the home route of this hash router
              <a href="#">{c}</a>
            ) : (
              <span>{c}</span>
            )}
          </Fragment>
        ))}
      </nav>
      <div className="row">{children}</div>
    </header>
  );
}

function Logo() {
  return (
    <svg className="logo" width={22} height={22} viewBox="0 0 24 24" aria-hidden="true">
      <rect x="7.5" y="2.5" width="14" height="14" rx="4" fill="var(--accent)" />
      <rect x="2.5" y="7.5" width="14" height="14" rx="4" fill="var(--accent-fill)" />
      <path
        d="M6.3 11.6l3.2 6 3.2-6"
        fill="none"
        stroke="var(--on-accent)"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

export function Box({
  title,
  action,
  children,
}: {
  title: React.ReactNode;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <section className="box">
      <div className="box-head">
        <h2>{title}</h2>
        {action && <div className="box-action">{action}</div>}
      </div>
      {children}
    </section>
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
        <div
          className="vid"
          style={{ aspectRatio: v.ar || 16 / 9, width: `min(100%, calc(52dvh * ${v.ar || 16 / 9}))` }}
        >
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
