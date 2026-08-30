import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  clearScreen: false,
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    host: '127.0.0.1',
    port: 34115,
  },
  test: {
    // Enables Testing Library's automatic cleanup between test cases.
    globals: true,
    // The pure decoder suites stay on plain node; component suites opt into a
    // DOM with a `@vitest-environment jsdom` docblock so the browser globals
    // never leak into the runtime-detection tests in src/lib.
    environment: 'node',
  },
})
