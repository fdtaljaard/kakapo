import { fileURLToPath, URL } from "node:url"
import { defineConfig } from "vite"
import react from "@vitejs/plugin-react"
import tailwindcss from "@tailwindcss/vite"

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    // One page, served locally from the exe: a 600 kB chunk is fine and the warning only trips build.ps1.
    chunkSizeWarningLimit: 1500,
  },
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  server: {
    // During development the Go backend runs on 7331 and Vite proxies the API to it.
    proxy: {
      "/api": "http://localhost:7331",
    },
  },
})
