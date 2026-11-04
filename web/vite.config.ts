import { execSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'

// Версия сборки в бандле (TZ UI v5 U02): короткий хэш коммита + время
// сборки. Встраивается через define, дублируется в dist/.build-info.json
// (читает pb doctor: чек web, U04; отдаёт /api/meta для сверки).
const commit = execSync('git describe --always --dirty', {
  encoding: 'utf8',
  stdio: ['ignore', 'pipe', 'inherit'],
}).trim()
const builtAt = new Date().toISOString()

function buildInfoPlugin(): Plugin {
  let outDir = 'dist'
  return {
    name: 'pb-build-info',
    apply: 'build',
    configResolved(cfg) {
      outDir = cfg.build.outDir
    },
    closeBundle() {
      mkdirSync(outDir, { recursive: true })
      writeFileSync(
        path.join(outDir, '.build-info.json'),
        JSON.stringify({ commit, built_at: builtAt }) + '\n',
      )
    },
  }
}

// Dev-сервер проксирует /api на backend (pb serve), CORS не нужен.
export default defineConfig({
  plugins: [react(), buildInfoPlugin()],
  define: {
    __PB_BUILD__: JSON.stringify({ commit, builtAt }),
  },
  server: {
    port: 5173,
    proxy: {
      // Порт backend = dashboard.listen из config (см. config.example.yaml).
      '/api': 'http://127.0.0.1:8090',
    },
  },
})
