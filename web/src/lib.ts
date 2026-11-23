// Every saved version, from `v2 diff`. Times are in each version's own timeline, as the agent logged them.

export type ChangeKind = "text" | "captions" | "graphics" | "video" | "voice" | "music" | "sfx";
export type ChangeType = "added" | "retimed";
export type Span = {
  from: number;
  to: number;
  text: string;
  kind?: ChangeKind | undefined;
  src?: string | undefined;
  type?: ChangeType | undefined;
};
export type Whole = { text: string; kind?: ChangeKind | undefined; src?: string | undefined };
export type Cut = { at: number; removed: number; text: string };
export type Version = {
  id: string;
  prompt: string;
  prompts: string[];
  saved: string;
  video: boolean;
  dur: number;
  // width / height as shown
  ar: number;
  changes: Span[];
  whole: Whole[];
  cuts: Cut[];
};
export type Comment = {
  id: string;
  version: string;
  at: number;
  text: string;
  drawing?: string;
  created_at: string;
  done_in?: string;
};

export type Item = {
  k: string;
  by: string;
  text: string;
  from: number;
  to: number;
  whole?: boolean;
  gone?: boolean;
  cut?: boolean;
  removed?: number;
};

type RawChange = {
  from?: number;
  to?: number;
  text: string;
  kind?: ChangeKind | "cut";
  src?: string;
  type?: ChangeType;
  removed?: number;
};
type RawVersion = {
  version: string;
  message: string;
  prompts?: string[];
  saved_at: string;
  video: boolean;
  duration: number;
  aspect: number;
  changes: RawChange[] | null;
};

const when = (iso: string) =>
  new Date(iso).toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });

export async function loadVersions(): Promise<{ project: string; versions: Version[] }> {
  const r = await fetch("/api/versions");
  if (!r.ok) throw new Error(await r.text());
  const data: { project: string; versions: RawVersion[] } = await r.json();
  return {
    project: data.project,
    versions: data.versions.map((x) => {
      const changes: Span[] = [];
      const whole: Whole[] = [];
      const cuts: Cut[] = [];
      for (const { from, to, text, kind, src, type, removed } of x.changes ?? []) {
        if (kind === "cut") {
          if (from !== undefined && removed !== undefined) cuts.push({ at: from, removed, text });
        } else if (from === undefined || to === undefined) whole.push({ text, kind, src });
        else changes.push({ from, to, text, kind, src, type });
      }
      return {
        id: x.version,
        prompt: x.message,
        prompts: x.prompts || [],
        saved: when(x.saved_at),
        video: x.video,
        dur: x.duration,
        ar: x.aspect,
        changes,
        whole,
        cuts,
      };
    }),
  };
}

export async function loadComments(): Promise<Comment[]> {
  const r = await fetch("/api/comments");
  if (!r.ok) throw new Error(await r.text());
  return r.json();
}

export const mmss = (s: number) =>
  `${Math.floor(Math.max(0, s) / 60)}:${String(Math.floor(Math.max(0, s) % 60)).padStart(2, "0")}`;
export const mmssd = (s: number) =>
  `${Math.floor(Math.max(0, s) / 60)}:${(Math.max(0, s) % 60).toFixed(1).padStart(4, "0")}`;
export const secs = (n: number) => `${n.toFixed(1).replace(/\.0$/u, "")} s`;
export const restoredFrom = (v: Version) => /^restore (v\d+)$/u.exec(v.prompt)?.[1];

// ---- time across versions ----
export class History {
  versions: Version[];
  byId: Record<string, Version>;
  constructor(versions: Version[]) {
    this.versions = versions;
    this.byId = Object.fromEntries(versions.map((v) => [v.id, v]));
  }
  num = (v: string) => this.versions.findIndex((x) => x.id === v);
  // A moment in version v → the same moment in the version before it (cut time comes back).
  stepBack = (V: Version, t: number) => t + V.cuts.filter((c) => c.at <= t + 1e-3).reduce((s, c) => s + c.removed, 0);
  // A moment in the version before v → the same moment in v, or null if v cut it.
  stepForward = (V: Version, t: number): number | null => {
    let off = 0;
    for (const c of V.cuts.toSorted((a, b) => a.at - b.at)) {
      const start = c.at + off;
      if (t < start) break;
      if (t < start + c.removed) return null;
      off += c.removed;
    }
    return t - off;
  };
  back = (from: string, to: string, t: number) => {
    for (const V of this.versions.slice(this.num(to) + 1, this.num(from) + 1).toReversed()) t = this.stepBack(V, t);
    return t;
  };
  forward = (from: string, to: string, t: number | null) => {
    for (const V of this.versions.slice(this.num(from) + 1, this.num(to) + 1)) {
      if (t === null) break;
      t = this.stepForward(V, t);
    }
    return t;
  };
  // A span made in version `from`, as it sits in version `to`. Parts that were later cut are trimmed off.
  spanIn = (from: string, to: string, a: number, b: number) => {
    const pts: number[] = [];
    for (let t = a; t <= b + 1e-6; t += 0.05) {
      const m = this.forward(from, to, Math.min(t, b));
      if (m !== null) pts.push(m);
    }
    return pts.length > 0 ? { from: Math.min(...pts), to: Math.max(...pts) } : null;
  };
  // Everything that changed after `base` (or just in `ver`, with no base), placed on `ver`'s timeline.
  changesSince = (ver: string, base?: string): Item[] => {
    const out: Item[] = [];
    const first = base ? this.num(base) + 1 : this.num(ver);
    for (const V of this.versions.slice(first, this.num(ver) + 1)) {
      const by = V.id;
      V.changes.forEach((c) => {
        const s = this.spanIn(by, ver, c.from, c.to);
        out.push(s ? { ...c, ...s, by, k: `k${out.length}` } : { ...c, by, gone: true, k: `k${out.length}` });
      });
      V.cuts.forEach((c) => {
        const moved = this.forward(by, ver, c.at);
        const at = moved ?? c.at;
        out.push({
          text: c.text,
          removed: c.removed,
          cut: true,
          by,
          from: at,
          to: at,
          gone: moved === null,
          k: `k${out.length}`,
        });
      });
      V.whole.forEach((w) => out.push({ text: w.text, whole: true, by, from: -1, to: -1, k: `k${out.length}` }));
    }
    return out.toSorted(
      (a, b) => +!!a.whole - +!!b.whole || +!!a.gone - +!!b.gone || a.from - b.from || this.num(a.by) - this.num(b.by),
    );
  };
}
