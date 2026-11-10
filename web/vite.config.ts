import { fileURLToPath } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

const src = fileURLToPath(new URL('./src', import.meta.url))
// Geist is self-hosted from the `geist` package: dbairlock runs inside private
// networks, so the app never requests fonts from a third party.
const geistFonts = fileURLToPath(new URL('./node_modules/geist/dist/fonts', import.meta.url))

const goServer = 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@/': `${src}/`,
      '@geist-fonts/': `${geistFonts}/`,
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      '/api': { target: goServer, changeOrigin: false },
      '/mcp': { target: goServer, changeOrigin: false },
      '/healthz': { target: goServer },
      '/readyz': { target: goServer },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    // One bundle for now; split by route when the feature pages grow.
    chunkSizeWarningLimit: 900,
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    css: false,
    restoreMocks: true,
  },
})
