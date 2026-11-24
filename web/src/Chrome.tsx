import { Fragment } from "react";
import { useEscapeTo } from "./hooks";

// The page's top bar. back: where the arrow (and Esc) goes, e.g. "#" for every version. Omitted on the home page.
export function Top({
  crumbs,
  back,
  children,
}: {
  crumbs: string[];
  back?: { href: string; label: string };
  children?: React.ReactNode;
}) {
  useEscapeTo(back?.href);
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
