import { defineConfig } from "vite";

// The page uses same-origin paths. AGUI_BACKEND_URL selects the Go server the
// dev proxy forwards to (default: the assistant-ui example on port 8092).
const backend = process.env.AGUI_BACKEND_URL ?? "http://127.0.0.1:8092";

export default defineConfig({
  server: {
    host: "127.0.0.1",
    proxy: Object.fromEntries(
      ["/agent", "/message", "/history", "/events", "/snapshot", "/observe", "/cancel", "/health"]
        .map((path) => [path, backend]),
    ),
  },
});
