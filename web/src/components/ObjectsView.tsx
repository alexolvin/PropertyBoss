import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  ApiError,
  lastServerTime,
  listObjects,
  type Meta,
  type ObjectItem,
  type ObjectsPage,
} from '../api'
import { formatMoney, formatPercent, formatPricePerSqm } from '../format'
import Dialog from './Dialog'
import Pager from './Pager'

interface Props {
  meta: Meta
  displayCurrency: string
}

const PER_PAGE = 20

export default function ObjectsView({ meta, displayCurrency }: Props) {
  const { t, i18n } = useTranslation()
  const locale = i18n.language

  // Применённые фильтры (список) и черновик в листе — отдельно:
  // список меняется только по «Применить» (макет Этапа 2).
  const [country, setCountry] = useState('')
  const [status, setStatus] = useState('')
  const [page, setPage] = useState(1)
  const [data, setData] = useState<ObjectsPage | null>(null)
  const [error, setError] = useState<ApiError | null>(null)
  // TZ UI v5 U09: скелетон — только если запрос длится > 300 мс
  // (быстрый ответ не мигает скелетоном).
  const [loading, setLoading] = useState(true)
  const [slow, setSlow] = useState(false)
  // TZ UI v5 U10: метка «данные на HH:MM» — время ответа сервера;
  // меняется только после успешного ответа.
  const [dataAt, setDataAt] = useState<Date | null>(null)
  const [reload, setReload] = useState(0)

  const [sheetOpen, setSheetOpen] = useState(false)
  const [draftCountry, setDraftCountry] = useState('')
  const [draftStatus, setDraftStatus] = useState('')
  const [draftTotal, setDraftTotal] = useState<number | null>(null)

  const load = () =>
    listObjects({
      country: country || undefined,
      status: status || undefined,
      page,
      per_page: PER_PAGE,
      display_currency: displayCurrency || undefined,
    })

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setSlow(false)
    const timer = setTimeout(() => {
      if (!cancelled) setSlow(true)
    }, 300)
    load()
      .then((d) => {
        if (cancelled) return
        setData(d)
        setError(null)
        setDataAt(lastServerTime())
      })
      .catch((e) => {
        if (cancelled) return
        setError(e instanceof ApiError ? e : new ApiError('network', String(e)))
      })
      .finally(() => {
        if (cancelled) return
        setLoading(false)
        setSlow(false)
      })
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [country, status, page, displayCurrency, reload])

  // Живой счётчик в листе фильтров: по черновику, один запрос на изменение;
  // total берётся из ответа (U29: подход per_page=1 упразднён — поле есть в
  // каждом ответе списка).
  useEffect(() => {
    if (!sheetOpen) return
    let cancelled = false
    listObjects({
      country: draftCountry || undefined,
      status: draftStatus || undefined,
      page: 1,
      per_page: 50,
      display_currency: displayCurrency || undefined,
    })
      .then((d) => {
        if (!cancelled) setDraftTotal(d.total)
      })
      .catch(() => {
        if (!cancelled) setDraftTotal(null)
      })
    return () => {
      cancelled = true
    }
  }, [sheetOpen, draftCountry, draftStatus, displayCurrency])

  const openSheet = () => {
    setDraftCountry(country)
    setDraftStatus(status)
    setSheetOpen(true)
  }
  const applyFilters = () => {
    setCountry(draftCountry)
    setStatus(draftStatus)
    setPage(1)
    setSheetOpen(false)
  }
  const resetFilters = () => {
    setDraftCountry('')
    setDraftStatus('')
    setCountry('')
    setStatus('')
    setPage(1)
  }
  const retry = () => setReload((r) => r + 1)

  // D24: рабочее состояние при повторном запросе — единственной
  // обратной связью была метка HH:MM, не меняющаяся внутри минуты
  // (клик выглядел мёртвым). Начальная загрузка не в счёт: у неё
  // свой индикатор — скелетон (U09), data ещё нет.
  const refreshing = loading && data !== null

  // TZ UI v5 U14 (E12): смена страницы НЕ меняет маршрут — scrollTo
  // на hashchange не срабатывает; скролл к началу списка явный.
  const goPage = (p: number) => {
    setPage(p)
    window.scrollTo(0, 0)
  }

  const expOf = (code: string | null | undefined) =>
    meta.currencies.find((c) => c.code === code)?.exponent ?? 2

  // Цена в валюте отображения (макет: валюта всегда указана, §7.5).
  const priceText = (o: ObjectItem): string | null => {
    if (displayCurrency && o.price_display) {
      return formatMoney(
        o.price_display.minor,
        o.price_display.currency,
        expOf(o.price_display.currency),
        locale,
      )
    }
    if (o.price_minor == null || !o.currency) return null
    return formatMoney(o.price_minor, o.currency, expOf(o.currency), locale)
  }

  // Отметка состояния — бейдж на обложке (TZ_UI v5 U16): только
  // ненормальное, одна на строку — «ушёл с рынка» имеет приоритет над трендом цены.
  const badgeText = (o: ObjectItem): string | null => {
    if (o.status === 'delisted') return t('objects.flag_delisted')
    if (o.price_trend === 'reduced') return t('objects.flag_price_reduced')
    if (o.price_trend === 'increased') return t('objects.flag_price_increased')
    return null
  }

  // Цена за квадратный метр (TZ_UI v5 U16) — производная величина:
  // валюта указана; при цене и отсутствии площади слот показывает
  // причину, а не пустоту (§8).
  const pricePerSqmText = (o: ObjectItem): string | null => {
    if (o.area_sqm == null || o.area_sqm <= 0) return null
    const sqm = t('search.sqm')
    if (displayCurrency && o.price_display) {
      return formatPricePerSqm(
        o.price_display.minor,
        o.price_display.currency,
        expOf(o.price_display.currency),
        locale,
        o.area_sqm,
        sqm,
      )
    }
    if (o.price_minor == null || !o.currency) return null
    return formatPricePerSqm(
      o.price_minor,
      o.currency,
      expOf(o.currency),
      locale,
      o.area_sqm,
      sqm,
    )
  }

  // Тип продавца в строке (TZ_UI v5 U16) — только когда известен.
  const sellerText = (o: ObjectItem): string | null =>
    o.seller_type
      ? t(`objects.seller_${o.seller_type}`, { defaultValue: o.seller_type })
      : null

  // Слот отклонения: число + компактный признак достоверности (§7.2)
  // или текст причины (§7.3) на месте числа. Компактная форма: размер
  // выборки числом + цветовая пометка при замещении зоны уровнем
  // родителя (полная форма — на экране объекта).
  const devSlot = (o: ObjectItem) => {
    const v = o.valuation
    if (v && v.price_deviation != null && v.interval_low_minor != null) {
      const cls =
        'dev num ' + (v.price_deviation < 0 ? 'neg' : v.price_deviation > 0 ? 'pos' : '')
      return (
        <>
          <span className={cls}>{formatPercent(v.price_deviation, locale)}</span>
          {/* TZ_UI v5 U20: замещение зоны — видимым текстом, не только
              в title= (на экране объекта — полная форма). */}
          <span className={'dev-meta-list num' + (v.zone_fallback ? ' fallback' : '')}>
            {t('objects.dev_meta', { n: v.sample_size })}
            {v.zone_fallback && `, ${t('objects.dev_fallback_compact')}`}
          </span>
        </>
      )
    }
    // Причина: код до «:» (в хвосте — параметры прогона).
    const prefix = v?.null_reason ? v.null_reason.split(':')[0].trim() : ''
    const reason =
      v == null
        ? t('objects.detail_reason_missing')
        : t(`objects.reason_${prefix}`, { defaultValue: v?.null_reason ?? '' })
    return <span className="dev">{reason}</span>
  }

  const pages = data ? Math.max(1, Math.ceil(data.total / data.per_page)) : 1

  // Пустой результат — с указанием действующего фильтра (критика Этапа 5):
  // «По условиям «Италия» не найдено…», а не абстрактно «по заданным условиям».
  const emptyFilter = [
    country ? t(`countries.${country}`, { defaultValue: country }) : '',
    status ? t(`objects.status_${status}`) : '',
  ]
    .filter(Boolean)
    .join(', ')
  const emptyText = emptyFilter
    ? t('objects.empty_text_filter', { filter: emptyFilter })
    : t('objects.empty_text')

  // TZ UI v5 U10: время из заголовка Date серверного ответа; формат —
  // локаль зрителя (источник — часы сервера, не клиента).
  const asOfText = dataAt
    ? t('objects.data_as_of', {
        time: new Intl.DateTimeFormat(locale, { hour: '2-digit', minute: '2-digit' }).format(dataAt),
      })
    : null

  return (
    <>
      <header className="list-top">
        {/* Счётчик скрыт при нуле: пустое состояние объясняет причину,
            «0 объектов» его дублирует и только размывает вину (критика Этапа 5) */}
        <span className="count num">
          {!data ? (
            null
          ) : data.total > 0 ? (
            <>
              <b>{data.total.toLocaleString(locale)}</b>{' '}
              {t('objects.count_word', { count: data.total })}
              {asOfText && <span className="asof">{asOfText}</span>}
            </>
          ) : null}
        </span>
        <span className="top-actions">
          {/* TZ UI v5 U10: «Обновить» — тот же запрос; метка актуальности
              меняется только после успешного ответа */}
          <button
            className="top-btn refresh-btn"
            onClick={retry}
            disabled={refreshing}
            aria-busy={refreshing || undefined}
          >
            {t('common.refresh')}
          </button>
          <button className="top-btn filter-btn" onClick={openSheet} aria-haspopup="dialog">
            {t('objects.filters')}
          </button>
        </span>
      </header>

      {/* TZ UI v5 U09: ошибка — причина человеческим языком и одна
          кнопка «Повторить», повторяющая тот же запрос; офлайн —
          сообщение, отличное от ошибки сервера */}
      {error && (
        <>
          <div className="error">
            {error.kind === 'offline'
              ? t('common.offline')
              : t('common.error', { message: error.message })}
          </div>
          <button className="ghost retry-btn" onClick={retry}>
            {t('common.retry')}
          </button>
        </>
      )}

      {/* TZ UI v5 U09: запрос > 300 мс — скелетон на месте будущего
          содержимого (3 строки для списка), не текст «Загрузка…» */}
      {!error && loading && slow && (
        <div className="skeleton-list" aria-hidden="true">
          <div className="skeleton-row" />
          <div className="skeleton-row" />
          <div className="skeleton-row" />
        </div>
      )}

      {data && data.objects.length === 0 && (
        <div className="empty">
          <h2>{t('objects.empty_title')}</h2>
          <p>{emptyText}</p>
          <button className="reset-btn" onClick={resetFilters}>
            {t('objects.empty_reset')}
          </button>
        </div>
      )}

      {data && data.objects.length > 0 && (
        <>
          <ul className="object-list">
            {data.objects.map((o, i) => {
              // TZ_UI v5 U15: обложка — первое фото (размер list); CLS=0
              // держит aspect-ratio в CSS. Ленивая загрузка — с 5-й строки:
              // первые четыре обложки должны быть в первом экране.
              const cover = o.cover_photo
              const coverSrc = cover
                ? (cover.list_url ?? cover.card_url ?? cover.original_url)
                : null
              const badge = badgeText(o)
              const perSqm = pricePerSqmText(o)
              const seller = sellerText(o)
              // Площадь и комнаты — отдельной строкой (не в адресе):
              // ключевые параметры не теряются в обрезке адреса (E9).
              const facts = [
                o.area_sqm != null ? `${o.area_sqm} ${t('search.sqm')}` : null,
                o.rooms != null ? t('objects.rooms_n', { count: o.rooms }) : null,
              ].filter(Boolean)
              return (
                <li key={o.id}>
                  <a className="row" href={`#/objects/${o.id}`}>
                    <span className="row-cover" aria-hidden="true">
                      {coverSrc ? (
                        <img
                          className="cover-img"
                          src={coverSrc}
                          alt=""
                          loading={i < 4 ? 'eager' : 'lazy'}
                        />
                      ) : (
                        <span className="cover-ph" />
                      )}
                      {badge && <span className="cover-badge">{badge}</span>}
                    </span>
                    <span className="row-main">
                      <span className="row-top">
                        <span className="dev-wrap">{devSlot(o)}</span>
                        <span className="row-price">
                          <span className={'price num' + (priceText(o) ? '' : ' empty')}>
                            {priceText(o) ?? t('objects.no_price')}
                          </span>
                          {perSqm ? (
                            <span className="price-sqm num">{perSqm}</span>
                          ) : priceText(o) && o.area_sqm == null ? (
                            <span className="price-sqm muted">
                              {t('objects.area_not_specified')}
                            </span>
                          ) : null}
                        </span>
                      </span>
                      {facts.length > 0 && <span className="row-facts num">{facts.join(', ')}</span>}
                      <span className="addr">{o.title || o.address || t('objects.untitled', { id: o.id })}</span>
                      {seller && <span className="row-seller">{seller}</span>}
                    </span>
                  </a>
                </li>
              )
            })}
          </ul>
          <Pager page={page} pages={pages} onPage={goPage} />
        </>
      )}

      {/* TZ UI v5 U07: шторка — примитив Dialog (фокус, Tab-цикл,
          Escape, возврат фокуса); закрытие тапом по фону сохранено */}
      {sheetOpen && <div className="sheet-backdrop" onClick={() => setSheetOpen(false)} />}
      <Dialog
        open={sheetOpen}
        label={t('objects.filter_title')}
        onClose={() => setSheetOpen(false)}
        className="sheet"
      >
        <div className="field">
          <label>
            {t('objects.filter_country')}
            <select
              value={draftCountry}
              onChange={(e) => setDraftCountry(e.target.value)}
            >
              <option value="">{t('objects.all_countries')}</option>
              {meta.countries.map((c) => (
                <option key={c} value={c}>
                  {t(`countries.${c}`, { defaultValue: c })}
                </option>
              ))}
            </select>
          </label>
        </div>
        <div className="field">
          <label>
            {t('objects.filter_status')}
            <select value={draftStatus} onChange={(e) => setDraftStatus(e.target.value)}>
              <option value="">{t('objects.all_status')}</option>
              <option value="active">{t('objects.status_active')}</option>
              <option value="delisted">{t('objects.status_delisted')}</option>
            </select>
          </label>
        </div>
        {/* Превью результата до применения (ТЗ §6.4) — с подписью: голое
            число дублировало счётчик шапки (критика Этапа 5) */}
        <p className="live-count num">
          {t('objects.filter_preview_label')}{' '}
          {draftTotal != null ? (
            <b>
              {draftTotal.toLocaleString(locale)}{' '}
              {t('objects.count_word', { count: draftTotal })}
            </b>
          ) : (
            t('common.loading')
          )}
        </p>
        <div className="sheet-actions">
          <button className="btn-ghost" onClick={resetFilters}>
            {t('objects.filter_reset')}
          </button>
          <button className="btn-primary" onClick={applyFilters}>
            {t('objects.filter_apply')}
          </button>
        </div>
      </Dialog>
    </>
  )
}
