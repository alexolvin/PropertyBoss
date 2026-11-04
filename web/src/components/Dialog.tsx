import { useEffect, useRef, type ReactNode } from 'react'

interface Props {
  open: boolean
  // Имя для ассистивных технологий (role=dialog требует aria-label/
  // aria-labelledby — видимого заголовка в шторке нет по макету).
  label: string
  onClose: () => void
  className?: string
  children: ReactNode
}

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

// TZ UI v5 U07: диалог как ОДИН переиспользуемый примитив по WAI-ARIA
// APG (pattern Dialog):
//   — фокус на первый управляющий элемент при открытии;
//   — Tab/Shift+Tab циклят внутри;
//   — Escape закрывает;
//   — при закрытии фокус возвращается вызвавшей кнопке;
//   — закрытие тапом по фону сохраняется (бэкдроп — на стороне
//     вызывающего, он же вызывает onClose).
// Второй реализации быть не должно: нижний лист Волны 4 и модалки
// Волны 2 (U22) переиспользуют этот компонент.
export default function Dialog({ open, label, onClose, className, children }: Props) {
  const ref = useRef<HTMLDivElement>(null)
  const lastActive = useRef<HTMLElement | null>(null)
  // onClose в ref: инлайновый проп меняется каждый рендер, а эффект
  // обязан зависеть только от open — иначе каждый рендер внутри
  // открытого диалога сбрасывал бы фокус на первый элемент.
  const closeRef = useRef(onClose)
  closeRef.current = onClose

  useEffect(() => {
    if (!open) return
    lastActive.current = document.activeElement as HTMLElement | null
    const el = ref.current
    const first = el?.querySelector<HTMLElement>(FOCUSABLE) ?? el
    first?.focus()

    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        closeRef.current()
        return
      }
      if (e.key !== 'Tab' || !el) return
      const items = Array.from(el.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
        (x) => x.checkVisibility(),
      )
      if (items.length === 0) {
        e.preventDefault()
        return
      }
      const firstEl = items[0]
      const lastEl = items[items.length - 1]
      const active = document.activeElement
      if (e.shiftKey) {
        if (active === firstEl || !el.contains(active as Node)) {
          e.preventDefault()
          lastEl.focus()
        }
      } else if (active === lastEl || !el.contains(active as Node)) {
        e.preventDefault()
        firstEl.focus()
      }
    }
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('keydown', onKey)
      // Фокус возвращается вызвавшей кнопке.
      lastActive.current?.focus()
    }
  }, [open])

  if (!open) return null
  return (
    <div ref={ref} className={className} role="dialog" aria-modal="true" aria-label={label}>
      {children}
    </div>
  )
}
