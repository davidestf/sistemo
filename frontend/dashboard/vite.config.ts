import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  base: '/dashboard/',
  resolve: {
    alias: {
      '$lib': path.resolve(__dirname, 'src/lib'),
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': 'http://localhost:7777',
      '/health': 'http://localhost:7777',
      '/terminals': { target: 'http://localhost:7777', ws: true },
    },
  },
})
