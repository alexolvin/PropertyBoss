import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

interface Props {
  page: number
  pages: number
  onPage: (page: number) => void
}

// Пагинация списков (TZ_UI v3 §2.8): статичная строка — без липкости.
// TZ UI v5 U11: кнопки несут и текст («назад/вперёд»), и стрелку ‹/›;
// в узком контейнере (< 400px, @container) текст скрывается и остаётся
// стрелка — строка пагинации не переносится на две линии (E11).
// Поле быстрого перехода остаётся: листать сотни страниц кнопками
// непрактично.
export default function Pager({ page, pages, onPage }: Props) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState(String(page))

  // Переход по полю — только по Enter/blur: ввод «15» не должно
  // прыгать на страницу 1 после первой цифры.
  useEffect(() => setDraft(String(page)), [page])

  const commit = () => {
    const n = Number(draft)
    setDraft(String(page))
    if (!Number.isInteger(n) || n < 1) return
    const p = Math.min(pages, n)
    if (p !== page) onPage(p)
  }

  if (pages <= 1) return null

  return (
    <nav className="pager" aria-label={t('common.prev_page')}>
      <button
        disabled={page <= 1}
        onClick={() => onPage(page - 1)}
        aria-label={t('common.prev_page')}
      >
        <span className="pager-word">{t('common.prev_short')}</span>
        <span className="pager-arrow" aria-hidden="true">
          ‹
        </span>
      </button>
      <span className="page-of num">
        {t('objects.page')} {page} {t('objects.of')} {pages}
      </span>
      <input
        className="pager-input num"
        type="number"
        inputMode="numeric"
        min={1}
        max={pages}
        value={draft}
        aria-label={t('objects.page')}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === 'Enter') commit()
        }}
      />
      <button
        disabled={page >= pages}
        onClick={() => onPage(page + 1)}
        aria-label={t('common.next_page')}
      >
        <span className="pager-word">{t('common.next_short')}</span>
        <span className="pager-arrow" aria-hidden="true">
          ›
        </span>
      </button>
    </nav>
  )
}
