import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Every path the Go server answers outside the SPA (api/router.go): the JSON API
// plus the server-rendered public routes.
const backendPaths = ["/api/", "/raw/", "/thumb/", "/hls/", "/s/", "/p/", "/d/", "/oembed"];

// In dev, the SPA runs on Vite and proxies backend paths to the Go server on
// :8080. In production the SPA is embedded into the Go binary and served by it.
//
// Vite matches a plain proxy key as a bare string prefix (url.startsWith(key)),
// and the proxy runs before Vite serves its own modules. So every key ends in a
// slash: a bare "/s" would also swallow /src/main.tsx, /settings and /setup and
// hand them to Go, leaving `npm run dev` on a blank page. "/oembed" is the one
// exception — it takes a query string, not a path segment, and no SPA route or
// Vite path starts with it.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: Object.fromEntries(backendPaths.map((p) => [p, "http://localhost:8080"])),
  },
});
