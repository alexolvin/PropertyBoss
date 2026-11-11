// TZ UI v5 §10 (U41/U42) — инвариантный слой над живой раздачей.
//
// 3 вьюпорта (320×568 / 390×844 / 430×932) × 2 цветовые схемы ×
// 2 локали = 12 проектов. Мобильный режим (isMobile/hasTouch):
// вьюпорт — физический, а не «окно браузера».
//
// baseURL — адрес serve. По умолчанию локальный адрес из config.example.yaml.
// Другой адрес только через PB_UI_BASE_URL, не в этом файле.
import { defineConfig } from '@playwright/test'

const VIEWPORTS = [
  { width: 320, height: 568 },
  { width: 390, height: 844 },
  { width: 430, height: 932 },
]
const SCHEMES = ['light', 'dark'] as const
const LOCALES = ['ru', 'en'] as const

export default defineConfig({
  testDir: './tests/ui',
  testIgnore: process.env.PB_UI_EXCLUDE_LIVE === '1' ? [/ui_live\.spec\.ts$/] : [],
  timeout: 60_000,
  expect: { timeout: 10_000 },
  fullyParallel: true,
  workers: 4,
  retries: 0,
  reporter: [['list'], ['html', { open: 'never' }]],
  outputDir: './test-results',
  use: {
    baseURL: process.env.PB_UI_BASE_URL ?? 'http://127.0.0.1:8091',
    isMobile: true,
    hasTouch: true,
    deviceScaleFactor: 2,
    trace: 'retain-on-failure',
    video: 'off',
  },
  projects: VIEWPORTS.flatMap((v) =>
    SCHEMES.flatMap((s) =>
      LOCALES.map((l) => ({
        name: `${v.width}-${s}-${l}`,
        use: {
          viewport: { width: v.width, height: v.height },
          colorScheme: s,
          locale: l === 'ru' ? 'ru-RU' : 'en-US',
        },
      })),
    ),
  ),
})
