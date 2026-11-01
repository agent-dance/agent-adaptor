import { defineConfig } from "vite";

export default defineConfig({
  server: {
    host: "127.0.0.1",
    proxy: Object.fromEntries(
      ["/agent", "/history", "/events", "/snapshot", "/observe", "/cancel", "/health"]
        .map((path) => [path, "http://127.0.0.1:8092"]),
    ),
  },
});
