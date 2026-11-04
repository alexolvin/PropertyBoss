// TZ UI v5 §10 (U41/U42) — инварианты раздачи: 5 маршрутов × 12 проектов
// (3 вьюпорта × 2 схемы × 2 локали).
//
// Приёмка U41: прогон обязан ПАДАТЬ на дереве до фиксов (E9 — обрезка
// .addr, E11 — pager на 2 строки, E5 — статичный html lang) и
// проходить после. Числа падений до/после — в отчёте волны.
import { test, expect, type Page } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import {
  assertNoHorizontalOverflow,
  assertTargetSizes,
  assertNoTruncation,
  assertLang,
  assertPagerSingleLine,
  runAxe,
} from './helpers'

// Прогон запускается из web/ (npm run test:ui). Один уровень вверх от
// web/ — корень репозитория. Неверный cwd однажды унёс скриншоты в
// родительский каталог (stray .a вне репозитория) — поэтому явная
// проверка, а не молчаливое падение в чужое место.
if (!fs.existsSync(path.join(process.cwd(), 'playwright.config.ts'))) {
  throw new Error('Playwright-тесты запускать из web/ (npm run test:ui)')
}
const SHOT_ROOT = path.join(process.cwd(), '..', '.a', 'reports', 'ui')

interface RouteDef {
  slug: string
  // url или '#/...' относительно baseURL; 'object' — id резолвится
  // runtime через API (активный объект CZ): маршрут не привязан к
  // конкретному id, который может delistиться.
  url: string
  wait: string
  pager?: boolean
}

const ROUTES: RouteDef[] = [
  { slug: 'objects', url: '/#/list', wait: '.object-list .row, .empty', pager: true },
  { slug: 'map', url: '/', wait: '.map-canvas, .map-page' },
  { slug: 'object', url: 'object', wait: '.obj-head' },
  { slug: 'models', url: '/#/models', wait: '.block' },
  { slug: 'calibration', url: '/#/models/calibration/CZ/sale', wait: '.calib-card' },
  { slug: 'settings', url: '/#/settings', wait: '.settings-block' },
]

// Первый активный объект CZ — из живого API (система, которую чекнем).
async function firstObjectId(page: Page): Promise<number> {
  const id = await page.evaluate(async () => {
    const r = await fetch('/api/objects?country=CZ&per_page=1', { headers: { accept: 'application/json' } })
    if (!r.ok) throw new Error(`api/objects: HTTP ${r.status}`)
    const j = (await r.json()) as { objects?: { id: number }[] }
    return j.objects?.[0]?.id ?? null
  })
  expect(id, 'нет ни одного активного объекта CZ — маршрут #/objects/:id не собрать').not.toBeNull()
  return id as number
}

for (const r of ROUTES) {
  test(r.slug, async ({ page }, testInfo) => {
    const [width, scheme, locale] = testInfo.project.name.split('-') as [
      string,
      'light' | 'dark',
      'ru' | 'en',
    ]

    // Локаль приложения — из localStorage('pb.locale'): i18n не читает
    // navigator.language, поэтому project-локаль прокидываем явно.
    await page.addInitScript((l) => localStorage.setItem('pb.locale', l), locale)

    if (r.url === 'object') {
      // id берём через API после загрузки (fetch внутри страницы).
      await page.goto('/')
      const id = await firstObjectId(page)
      await page.evaluate((u) => { location.hash = u }, `#/objects/${id}`)
    } else {
      await page.goto(r.url)
    }

    await page.waitForSelector(r.wait, { timeout: 20_000 })
    // Ошибка маршрута (App-level <div class="error"> — прямой ребёнок
    // main) не должна маскироваться зелёными assertions на пустой странице.
    expect(await page.locator('main > .error').count(), 'маршрут в состоянии ошибки').toBe(0)

    // Assertion 5 — скриншот каждого прогона (фиксация, ДО assertions):
    // .a/reports/ui/<дата>/<маршрут>_<ширина>_<схема>_<локаль>.png
    const shot = path.join(
      SHOT_ROOT,
      new Date().toISOString().slice(0, 10),
      `${r.slug}_${width}_${scheme}_${locale}.png`,
    )
    await page.screenshot({ path: shot, fullPage: false })

    await assertNoHorizontalOverflow(page)
    await assertTargetSizes(page)
    await assertNoTruncation(page)
    await assertLang(page, locale)
    if (r.pager) await assertPagerSingleLine(page)
    await runAxe(page)
  })
}
