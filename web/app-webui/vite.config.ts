import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  server: {
    port: 5173,
    proxy: { '/api': { target: process.env.INGOT_API_URL || 'http://127.0.0.1:7316' } },
  },
  build: {
    outDir: fileURLToPath(new URL('../../plugins/app-webui/app/webdist', import.meta.url)),
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks: {
          markdown: ['markdown-it', 'highlight.js/lib/common'],
          components: ['reka-ui'],
        },
      },
    },
  },
  test: { environment: 'jsdom', include: ['src/**/*.test.ts'], setupFiles: ['./src/test-setup.ts'], restoreMocks: true },
})
