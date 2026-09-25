import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

// The build goes to dist/, which the Go package in this directory embeds.
// `npm run dev` proxies the API to a proxy running locally on :18080.
export default defineConfig({
  plugins: [react()],
  base: './',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // One small app: a single JS and CSS file keep the embed simple.
    chunkSizeWarningLimit: 1024,
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:18080',
    },
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
})
