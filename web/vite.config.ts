import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The Go aggregator listens on :8080 with no auth and no configuration, so the
// dev server just forwards /api and /ws straight through. Same-origin in dev
// keeps the websocket URL derivable from window.location in both dev and prod.
const backend = 'http://localhost:8080'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: backend, changeOrigin: true },
      '/healthz': { target: backend, changeOrigin: true },
      // The websocket entry targets ws:// directly. Pointing it at the http
      // origin makes the proxy answer the upgrade with a 500 before it ever
      // reaches Go.
      '/ws': { target: backend.replace(/^http/, 'ws'), ws: true },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
  },
})
