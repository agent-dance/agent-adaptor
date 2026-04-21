import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";

// https://vitejs.dev/config/
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  return {
    plugins: [react()],
    define: {
      __AGENT_BACKEND_URL__: JSON.stringify(
        env.AGENT_BACKEND_URL ?? "http://localhost:8090/agent",
      ),
    },
    server: {
      port: 5173,
    },
  };
});
