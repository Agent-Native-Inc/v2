import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The build lands inside the Go package, which embeds it into the v2 binary.
// `npm run dev` proxies to a running `v2 diff --port 7777 --no-open` in any project.
const v2 = "http://127.0.0.1:7777";
export default defineConfig({
  plugins: [react()],
  build: { outDir: "../cmd/v2/dist", emptyOutDir: true },
  server: { proxy: { "/api": v2, "/video": v2, "/poster": v2, "/peaks": v2, "/comments": v2 } },
});
