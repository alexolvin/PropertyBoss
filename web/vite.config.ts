import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Dev-сервер проксирует /api на backend (pb serve), CORS не нужен.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      // Порт backend = dashboard.listen из config (см. config.example.yaml).
      '/api': 'http://127.0.0.1:8090',
    },
  },
})
