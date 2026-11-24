import { createRoot } from "react-dom/client";
import { useEffect, useState } from "react";
// oxlint-disable-next-line import/no-unassigned-import -- a stylesheet is imported for its side effect
import "./styles.css";
import { History, loadComments, loadVersions, type Comment } from "./lib";
import { Feed } from "./Feed";
import { CommentMode } from "./CommentMode";
import { Compare } from "./Compare";

// One page. No hash: every version. #v3: comment mode on v3. #v3/vs/v1: v3 side by side with v1.
function App() {
  const [data, setData] = useState<{ project: string; h: History }>();
  const [comments, setComments] = useState<Comment[]>([]);
  const [error, setError] = useState("");
  const [hash, setHash] = useState(location.hash);

  useEffect(() => {
    const onHash = () => setHash(location.hash);
    addEventListener("hashchange", onHash);
    const load = async () => {
      const [v, c] = await Promise.all([loadVersions(), loadComments()]);
      setData({ project: v.project, h: new History(v.versions) });
      setComments(c);
    };
    load().catch((e: Error) => setError(e.message));
    return () => removeEventListener("hashchange", onHash);
  }, []);

  if (error)
    return (
      <main className="wrap">
        <h1>Couldn&apos;t load the versions</h1>
        <p className="muted">{error}</p>
      </main>
    );
  if (!data) return null;
  const { project, h } = data;
  if (h.versions.length === 0)
    return (
      <main className="wrap">
        <h1>No versions yet</h1>
        <p className="muted">
          Save one with <code>v2 save</code>, then refresh this page.
        </p>
      </main>
    );

  const [ver = "", , base = ""] = hash.replace(/^#\/?/u, "").split("/");
  const V = h.byId[ver];
  const B = h.byId[base];
  const refresh = () => loadComments().then(setComments);
  if (V && B?.video && h.num(base) < h.num(ver)) return <Compare key={hash} project={project} h={h} A={V} B={B} />;
  if (V) return <CommentMode key={ver} project={project} V={V} comments={comments} refresh={refresh} />;
  return <Feed project={project} h={h} comments={comments} />;
}

const root = document.querySelector("#root");
if (root) createRoot(root).render(<App />);
