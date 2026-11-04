// TZ UI v5 §10 (U41/U42) — общие assertions инвариантного слоя.
//
// Каждый assertion — именованный инвариант (как чеки doctor): либо
// проходит, либо падает с точной причиной. Списки исключений —
// явные константы: любое дополнение обязано нести комментарий,
// почему элемент исключён (ТЗ: «список исключений — пустой» для
// target-size; для truncation ТЗ перечисляет явно: цена, дата).
import { expect, type Page } from '@playwright/test'
import path from 'node:path'

// TZ §11: минимальный тач-таргет.
export const TOUCH = 44

// U41, assertion 2: интерактивные элементы ≥ 44×44.
// Список исключений ПУСТ (ТЗ). Любая запись — только с why.
const TARGET_EXCLUSIONS: { selector: string; why: string }[] = []

// U41, assertion 3: нет РЕАЛЬНОГО обрезания nowrap-текста. Исключения
// — явно перечисленные в ТЗ: цена, дата.
const TRUNC_EXCLUSIONS: { selector: string; why: string }[] = [
  {
    selector: '.price',
    why: 'цена (ТЗ §11: nowrap в контракте; ellipsis — допустимый выход для одной строки цены)',
  },
]

// 1. Нет горизонтального переполнения страницы.
export async function assertNoHorizontalOverflow(page: Page): Promise<void> {
  const m = await page.evaluate(() => ({
    docScroll: document.documentElement.scrollWidth,
    docClient: document.documentElement.clientWidth,
    bodyScroll: document.body.scrollWidth,
    win: window.innerWidth,
  }))
  expect(
    m.docScroll,
    `horiz overflow: document.scrollWidth ${m.docScroll} > clientWidth ${m.docClient}`,
  ).toBeLessThanOrEqual(m.docClient)
  expect(
    m.bodyScroll,
    `horiz overflow: body.scrollWidth ${m.bodyScroll} > window.innerWidth ${m.win}`,
  ).toBeLessThanOrEqual(m.win)
}

// 2. Все интерактивные элементы ≥ TOUCH×TOUCH.
export async function assertTargetSizes(page: Page): Promise<void> {
  const bad = await page.evaluate(
    ({ min, exclusions }) => {
      const out: { tag: string; cls: string; w: number; h: number; label: string }[] = []
      const sel = 'a, button, input, select, textarea, [role="button"], [tabindex]:not([tabindex="-1"])'
      for (const el of Array.from(document.querySelectorAll<HTMLElement>(sel))) {
        if (!el.checkVisibility()) continue
        if (exclusions.some((s) => el.matches(s))) continue
        const r = el.getBoundingClientRect()
        if (r.width >= min && r.height >= min) continue
        out.push({
          tag: el.tagName.toLowerCase(),
          cls: typeof el.className === 'string' ? el.className.trim().split(/\s+/).join('.') : '',
          w: Math.round(r.width * 10) / 10,
          h: Math.round(r.height * 10) / 10,
          label: (el.getAttribute('aria-label') || el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 40),
        })
      }
      return out
    },
    { min: TOUCH, exclusions: TARGET_EXCLUSIONS.map((x) => x.selector) },
  )
  expect(bad, `элементы < ${TOUCH}×${TOUCH}: ${JSON.stringify(bad)}`).toEqual([])
}

// 3. Нет РЕАЛЬНОГО переполнения nowrap-текста (кроме цены/даты).
//
// Нюанс, пойманный baseline-прогоном (E9): `.addr` — INLINE-спэн
// (white-space:nowrap + overflow:hidden + ellipsis в CSS, но для
// inline-элементов overflow/text-overflow бездействуют, а
// clientWidth/scrollWidth всегда 0). Текст не «обрезаится самим
// элементом» — он ВЫХОДИТ за содержащий бокс (родитель). Поэтому:
//   block/flex-item — scrollWidth > clientWidth;
//   inline — rect текстового Range сравнивается с боксом родителя.
// Ищем элементы с НАМЕРОМ обрезать (ellipsis или overflow:hidden
// + nowrap) и проверяем, что намерение реально не выполняется.
export async function assertNoTruncation(page: Page): Promise<void> {
  const flagged = await page.evaluate((exclusions) => {
    const out: { cls: string; display: string; overflowPx: number; text: string }[] = []
    for (const el of Array.from(document.querySelectorAll<HTMLElement>('body *'))) {
      if (!el.checkVisibility()) continue
      const cs = getComputedStyle(el)
      if (cs.whiteSpace !== 'nowrap') continue
      if (cs.textOverflow !== 'ellipsis' && cs.overflow !== 'hidden') continue
      if (exclusions.some((s) => el.matches(s))) continue
      // Только элементы со своим текстом (не чистые контейнеры).
      const ownText = Array.from(el.childNodes).some(
        (n) => n.nodeType === Node.TEXT_NODE && (n.textContent || '').trim() !== '',
      )
      if (!ownText) continue
      let overflow: number
      if (cs.display === 'inline') {
        const range = document.createRange()
        range.selectNodeContents(el)
        const tr = range.getBoundingClientRect()
        const pr = el.parentElement ? el.parentElement.getBoundingClientRect() : null
        if (tr.width === 0 || !pr) continue
        overflow = tr.right - (pr.left + pr.width)
      } else {
        overflow = el.scrollWidth - el.clientWidth
      }
      if (overflow <= 1) continue
      out.push({
        cls: typeof el.className === 'string' ? el.className.trim().split(/\s+/).join('.') : el.tagName.toLowerCase(),
        display: cs.display,
        overflowPx: Math.round(overflow),
        text: (el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 60),
      })
    }
    return out
  }, TRUNC_EXCLUSIONS.map((x) => x.selector))
  expect(
    flagged,
    `nowrap-текст реально переполняет бокс (E9-класс): ${JSON.stringify(flagged)}`,
  ).toEqual([])
}

// 4. html lang = текущей локали UI.
export async function assertLang(page: Page, locale: 'ru' | 'en'): Promise<void> {
  const lang = await page.evaluate(() => document.documentElement.lang)
  expect(
    lang,
    `html lang="${lang}" ≠ локали UI "${locale}" (E5: lang обязана обновляться со сменой локали)`,
  ).toBe(locale)
}

// 6 (маршрутный): pager — одна строка.
// TЗ U41 перечисляет 5 общих assertions, но приёмка требует поймать
// E11 (pager на 2 строки при 320px): обёртка строки не ловится
// ни одним общим (кнопки ≥44px, перенос ≠ горизонтальный overflow,
// nowrap у .page-of не обрывает текст).
// Критерий «одна строка» — ВЫСОТА .pager: дети разной высоты
// центрируются (align-items: center) и имеют РАЗНЫЕ offsetTop
// даже на одной строке; при переносе высота складывается из двух
// строк (44+44+gap ≈ 96 > TOUCH+4).
export async function assertPagerSingleLine(page: Page): Promise<void> {
  const res = await page.evaluate(({ max }) => {
    const pager = document.querySelector<HTMLElement>('nav.pager')
    if (!pager) return null
    if (pager.offsetHeight <= max) return null
    const tops = Array.from(new Set(Array.from(pager.children).map((c) => c.offsetTop)))
    return { tops, height: pager.offsetHeight }
  }, { max: TOUCH + 4 })
  expect(res, `pager перенесён на 2 строки (E11): ${JSON.stringify(res)}`).toBeNull()
}

// U42: axe-core — ноль serious/critical на маршруте.
const AXE_PATH = path.resolve(process.cwd(), 'node_modules', 'axe-core', 'axe.min.js')

export async function runAxe(page: Page): Promise<void> {
  await page.addScriptTag({ path: AXE_PATH })
  const violations = await page.evaluate(async () => {
    const r = await (window as unknown as { axe: { run: (t: unknown, o: unknown) => Promise<unknown> } }).axe.run(
      document,
      { resultTypes: ['violations'] },
    )
    const v = (r as { violations: { id: string; impact: string; help: string; nodes: unknown[] }[] }).violations
    return v
      .filter((x) => x.impact === 'serious' || x.impact === 'critical')
      .map((x) => ({ id: x.id, impact: x.impact, help: x.help, nodes: x.nodes.length }))
  })
  expect(
    violations,
    `axe-core serious/critical: ${JSON.stringify(violations)}`,
  ).toEqual([])
}
