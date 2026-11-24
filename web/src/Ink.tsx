import { useCallback, useEffect, useRef } from "react";

// points as fractions of the frame, so they survive resizing
export type Stroke = [number, number][];

const point = (e: React.PointerEvent<HTMLCanvasElement>): [number, number] => {
  const r = e.currentTarget.getBoundingClientRect();
  return [(e.clientX - r.left) / r.width, (e.clientY - r.top) / r.height];
};

// A canvas over the video to draw on while `on`; each finished stroke goes to onStroke.
export function Ink({ on, strokes, onStroke }: { on: boolean; strokes: Stroke[]; onStroke: (s: Stroke) => void }) {
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
  return (
    <canvas
      ref={canvas}
      className={on ? "ink on" : "ink"}
      onPointerDown={(e) => {
        if (!on) return;
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
        if (done) onStroke(done);
      }}
    />
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
export function frameWithStrokes(v: HTMLVideoElement, strokes: Stroke[]): Promise<Blob> {
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
