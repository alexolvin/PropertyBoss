import { Fragment, lazy, Suspense, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ApiError, getObject, type Meta, type ObjectItem, type Photo } from '../api'
import { formatDate, formatMoney, formatPercent, formatPricePerSqm } from '../format'
import Dialog from './Dialog'

const MiniMap = lazy(() => import('./MiniMap'))

interface Props {
  id: number
  meta: Meta
  displayCurrency: string
}

// Хост ссылки на объявление (макет Этапа 2: в кнопке — домен площадки).
function hostOf(url: string): string | null {
  try {
    return new URL(url).hostname || null
  } catch {
    return null
  }
}

/**
 * Экран объекта (TZ_UI v3 §6.3): блоки по макету Этапа 2 —
 * оценка, цена, уход с рынка, характеристики, расположение,
 * объявление на площадке, служебные сведения.
 * Число модели всегда с признаком достоверности (ТЗ §7.1):
 * модель + выборка + замещение зоны в метке под числом.
 */
export default function ObjectDetailView({ id, meta, displayCurrency }: Props) {
  const { t, i18n } = useTranslation()
  const locale = i18n.language
  const [data, setData] = useState<ObjectItem | null>(null)
  const [error, setError] = useState<ApiError | null>(null)
  // TZ UI v5 U09: скелетон — только если запрос длится > 300 мс.
  const [loading, setLoading] = useState(true)
  const [slow, setSlow] = useState(false)
  const [reload, setReload] = useState(0)
  // TZ_UI v5 U17: фид фото + полноэкранный просмотр (примитив U07).
  const [feedIdx, setFeedIdx] = useState(0)
  const [viewerOpen, setViewerOpen] = useState(false)
  const [viewerIdx, setViewerIdx] = useState(0)
  const feedRef = useRef<HTMLDivElement>(null)
  const viewerFeedRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    setFeedIdx(0)
    setViewerOpen(false)
    let cancelled = false
    setLoading(true)
    setSlow(false)
    const timer = setTimeout(() => {
      if (!cancelled) setSlow(true)
    }, 300)
    getObject(id)
      .then((d) => {
        if (!cancelled) {
          setData(d)
          setError(null)
        }
      })
      .catch((e) => {
        if (!cancelled) setError(e instanceof ApiError ? e : new ApiError('network', String(e)))
      })
      .finally(() => {
        if (!cancelled) {
          setLoading(false)
          setSlow(false)
        }
      })
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [id, reload])

  // При открытии — скролл полноэкранного фида к нажатому фото
  // (хук — до ранних return, с остальными).
  useEffect(() => {
    if (viewerOpen && viewerFeedRef.current) {
      const el = viewerFeedRef.current
      el.scrollTo({ left: viewerIdx * el.clientWidth })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [viewerOpen])

  const expOf = (code: string | null | undefined) =>
    meta.currencies.find((c) => c.code === code)?.exponent ?? 2

  // TZ UI v5 U09: ошибка — причина человеческим языком и одна кнопка
  // «Повторить»; офлайн — сообщение, отличное от ошибки сервера.
  if (error) {
    return (
      <>
        <div className="error">
          {error.kind === 'offline'
            ? t('common.offline')
            : t('common.error', { message: error.message })}
        </div>
        <button className="ghost retry-btn" onClick={() => setReload((r) => r + 1)}>
          {t('common.retry')}
        </button>
      </>
    )
  }
  if (!data) {
    // TZ UI v5 U09: запрос > 300 мс — скелетон-блок, не текст «Загрузка…».
    return loading && slow ? <div className="skeleton-block" aria-hidden="true" /> : null
  }

  const o = data
  const v = o.valuation
  const h = o.hazard

  // --- Фото (TZ_UI v5 U17, ТЗ v4 §4.4) ------------------------------------
  // Фид — первый блок экрана: горизонтальный, листается по одному фото
  // (scroll-snap), счётчик «текущее / всего»; тап — полноэкранный.
  // Локальная копия: card (list — для строк списка); fallback — источник.
  const photos = o.photos ?? []
  const photoSrc = (p: Photo): string => p.card_url ?? p.list_url ?? p.original_url
  const openViewer = (i: number) => {
    setViewerIdx(i)
    setViewerOpen(true)
  }
  const closeViewer = () => setViewerOpen(false)
  // Счётчики фидов — по скроллу (какой кадр в центре); ref читаем в
  // момент события, не в момент рендера.
  const onFeedScroll = () => {
    const el = feedRef.current
    if (el) setFeedIdx(Math.max(0, Math.round(el.scrollLeft / Math.max(1, el.clientWidth))))
  }
  const onViewScroll = () => {
    const el = viewerFeedRef.current
    if (el) setViewerIdx(Math.max(0, Math.round(el.scrollLeft / Math.max(1, el.clientWidth))))
  }

  // Цена в валюте отображения (макет: валюта всегда указана, §7.5).
  const priceOf = (minor: number, currency: string | null): string =>
    formatMoney(minor, currency, expOf(currency), locale)
  const currentPrice =
    displayCurrency && o.price_display
      ? formatMoney(o.price_display.minor, o.price_display.currency, expOf(o.price_display.currency), locale)
      : o.price_minor != null && o.currency
        ? priceOf(o.price_minor, o.currency)
        : null

  // Краткая причина (в хвосте null_reason — параметры прогона).
  const reasonPrefix = (r: string | null | undefined) =>
    (r ?? '').split(':')[0].trim()

  // --- Блок «Оценка» ---------------------------------------------------
  // Число + интервал + знак достоверности (ТЗ §7.1–7.2): без интервала
  // число не показываем.
  const hasDeviation =
    v != null &&
    v.price_deviation != null &&
    v.interval_low_minor != null &&
    v.interval_high_minor != null
  const valuationBlock = (
    <section className="block">
      <h2>{t('objects.detail_valuation')}</h2>
      {/* C25: not_modeled — объект без уровня геокода структурно не
          моделируем; публикуемых оценок не несёт (API возвращает
          valuation=null). Честная пометка вместо «оценка недоступна». */}
      {o.not_modeled ? (
        <div className="dev-state small">
          {t('objects.detail_not_modeled')}
        </div>
      ) : hasDeviation && v != null && v.price_deviation != null && v.interval_low_minor != null && v.interval_high_minor != null ? (
        <>
          <div
            className={
              'dev-state num ' +
              (v.price_deviation < 0 ? 'neg' : v.price_deviation > 0 ? 'pos' : '')
            }
          >
            {formatPercent(v.price_deviation, locale)}
          </div>
          <p className="dev-reason">
            {t('objects.deviation_value', {
              dev: formatPercent(v.price_deviation, locale),
            })}
          </p>
          <p className="dev-meta num">
            {t('objects.interval_line', {
              low: priceOf(v.interval_low_minor, o.currency),
              high: priceOf(v.interval_high_minor, o.currency),
            })}
            <br />
            {v.model_version && t('objects.model_version', { version: v.model_version })}
            <br />
            {t('objects.sample_line', { n: v.sample_size })}
            {v.r_squared != null && (
              <>
                <br />
                {t('objects.r2_line', { r2: v.r_squared.toLocaleString(locale) })}
              </>
            )}
            <br />
            {t('objects.zone_fallback', {
              value: v.zone_fallback
                ? t('objects.zone_fallback_yes')
                : t('objects.zone_fallback_no'),
            })}
          </p>
        </>
      ) : (
        <>
          <div className="dev-state small">
            {t('objects.valuation_unavailable')}
          </div>
          <p className="dev-reason">
            {v == null
              ? t('objects.detail_reason_missing')
              : t(`objects.detail_reason_${reasonPrefix(v.null_reason)}`, {
                  defaultValue: v.null_reason ?? t('objects.detail_reason_unknown'),
                })}
          </p>
          {v?.model_version && (
            <p className="dev-meta">
              {t('objects.model_version', { version: v.model_version })}
              <br />
              {t('objects.zone_fallback', {
                value: v.zone_fallback
                  ? t('objects.zone_fallback_yes')
                  : t('objects.zone_fallback_no'),
              })}
            </p>
          )}
        </>
      )}
    </section>
  )

  // --- Блок «Цена» ------------------------------------------------------
  // Цена + цена/м² + история (TZ_UI v5 U18). Цена/м² — производная:
  // только когда и цена, и площадь есть.
  const perSqm = (() => {
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
    return formatPricePerSqm(o.price_minor, o.currency, expOf(o.currency), locale, o.area_sqm, sqm)
  })()
  const hist = o.price_history ?? []
  const histRows = hist.map((p) => (
    <div className="hist-row" key={p.change_at}>
      <span className="hist-date">{formatDate(p.change_at, locale)}</span>
      <span className="num">{priceOf(p.price_minor, p.currency)}</span>
    </div>
  ))
  let histDelta: string | null = null
  if (hist.length >= 2) {
    const first = hist[0]
    const last = hist[hist.length - 1]
    const deltaMinor = last.price_minor - first.price_minor
    if (deltaMinor !== 0 && first.price_minor !== 0) {
      const pct = deltaMinor / first.price_minor
      const key = deltaMinor < 0 ? 'objects.price_change_down' : 'objects.price_change_up'
      histDelta = t(key, {
        delta: priceOf(Math.abs(deltaMinor), last.currency),
        // Процент — со знаком изменения (критика Этапа 5): «Снижение за
        // период: 30 000 CZK (−2,3 %)», а не «(−2,3 %)» с плюсом.
        // Цвет остаётся нейтральным: ТЗ §8 — цвет только для ценового
        // отклонения, изменение цены — другое понятие.
        pct: formatPercent(pct, locale),
      })
    }
  }
  const priceBlock = (
    <section className="block">
      <h2>{t('objects.detail_price')}</h2>
      <div className={'price-now num' + (currentPrice ? '' : ' empty')}>
        {currentPrice ?? t('objects.no_price')}
      </div>
      {perSqm && <p className="price-sqm num">{perSqm}</p>}
      {/* D9 (журнал дефектов, ТЗ v4 §6.4.3): одна запись истории — это не
          «история», а повтор текущей цены. Блок показываем от двух записей. */}
      {hist.length >= 2 && (
        <>
          <div className="hist">{histRows}</div>
          {histDelta && <p className="hist-delta">{histDelta}</p>}
        </>
      )}
    </section>
  )

  // --- Блок «Уход с рынка» ----------------------------------------------
  const hazardBlock = (
    <section className="block">
      <h2>{t('objects.detail_hazard')}</h2>
      {h?.probability != null && h.horizon_days != null ? (
        <>
          <div className="dev-state small num">{formatPercent(h.probability, locale)}</div>
          <p className="dev-reason">
            {t('objects.hazard_leave', { horizon: h.horizon_days })}
          </p>
          <p className="dev-meta">
            {h.model_version && (
              <>
                {t('objects.model_version', { version: h.model_version })}
                <br />
              </>
            )}
            {t('objects.hazard_events', { n: h.events_in_training })}
          </p>
        </>
      ) : (
        <>
          <div className="dev-state small">
            {t('objects.hazard_unavailable')}
          </div>
          <p className="dev-reason">
            {h?.null_reason
              ? t(`objects.hazard_reason_${reasonPrefix(h.null_reason)}_full`, {
                  n: h.events_in_training,
                  min: 100,
                  horizon: h.horizon_days ?? '',
                  reason: h.model_version ?? '',
                  defaultValue: h.null_reason,
                })
              : t('objects.hazard_reason_missing')}
          </p>
          {/* Признак достоверности — и в недоступном состоянии (критика
              Этапа 5): пользователь должен понимать, от какой модели это
              «недоступно» */}
          {h?.model_version && (
            <p className="dev-meta num">
              {t('objects.model_version', { version: h.model_version })}
              {h.horizon_days != null && (
                <>
                  <br />
                  {t('objects.hazard_horizon', { horizon: h.horizon_days })}
                </>
              )}
            </p>
          )}
        </>
      )}
    </section>
  )

  // --- Блок «Характеристики» --------------------------------------------
  // D1 (журнал дефектов, ТЗ v4 §6.4.5, §8.3): отсутствующие атрибуты —
  // ОДНОЙ строкой «Не указано: …», а не «нет данных» на каждой строке.
  const attrPresent: Array<[string, string]> = []
  if (o.area_sqm != null)
    attrPresent.push([t('objects.attr_area'), `${o.area_sqm.toLocaleString(locale)} ${t('search.sqm')}`])
  if (o.rooms != null) attrPresent.push([t('objects.attr_rooms'), t('objects.rooms_n', { count: o.rooms })])
  if (o.property_type)
    attrPresent.push([
      t('objects.attr_type'),
      t(`objects.type_${o.property_type}`, { defaultValue: o.property_type }),
    ])
  const extraAttrs = { ...(o.attributes ?? {}), ...(o.attributes_unmapped ?? {}) }
  const skipExtra = new Set(['area_sqm', 'rooms', 'seller_type', 'offer_type'])
  const pushExtra = (k: string, asSqm = false) => {
    const v = extraAttrs[k]
    if (v == null || v === '' || skipExtra.has(k)) return
    if (k === 'floor' && Number(v) === 0) return
    if (k === 'total_area' && o.area_sqm != null && Number(v) === Number(o.area_sqm)) return
    const shown = extraAttrs[k + '_label'] ?? v
    const snake = k.replace(/[A-Z]/g, (m) => `_${m.toLowerCase()}`)
    const label = t(`objects.attr_${k}`, {
      defaultValue: t(`objects.attr_${snake}`, { defaultValue: k }),
    })
    let text: string
    if (asSqm && typeof v === 'number') {
      text = `${v.toLocaleString(locale)} ${t('search.sqm')}`
    } else {
      text = fmtExtra(shown)
    }
    attrPresent.push([label, text])
    skipExtra.add(k)
  }
  const fmtExtra = (v: unknown): string => {
    if (typeof v === 'boolean') return v ? t('objects.yes') : t('objects.no')
    if (Array.isArray(v)) return v.map((x) => fmtExtra(x)).join(', ')
    if (typeof v === 'string') {
      const low = v.trim().toLowerCase()
      if (low === 'ne' || low === 'no') return t('objects.no')
      if (low === 'ano' || low === 'yes') return t('objects.yes')
      const mapped = t(`objects.val_${v}`, { defaultValue: '' })
      if (mapped) return mapped
    }
    return String(v)
  }
  // Участок, вода, отопление — сразу после площади/комнат: иначе тонут
  // в хвосте unmapped и визуально «не указаны».
  pushExtra('plot_area', true)
  pushExtra('total_area', true)
  pushExtra('water')
  pushExtra('heating')
  pushExtra('gully')
  pushExtra('energy_class')
  pushExtra('condition')
  pushExtra('building_material')
  pushExtra('floors')
  for (const [k, v] of Object.entries(extraAttrs)) {
    if (skipExtra.has(k) || v == null || v === '' || k.endsWith('_label')) continue
    if (k === 'floor' && Number(v) === 0) continue
    const labelKey = k.endsWith('_label') ? k.slice(0, -6) : k
    const shown = extraAttrs[k + '_label'] ?? v
    const snake = labelKey.replace(/[A-Z]/g, (m) => `_${m.toLowerCase()}`)
    const label = t(`objects.attr_${labelKey}`, {
      defaultValue: t(`objects.attr_${snake}`, { defaultValue: labelKey }),
    })
    attrPresent.push([label, fmtExtra(shown)])
  }
  const attrMissing = [
    o.area_sqm == null && t('objects.attr_area'),
    o.rooms == null && t('objects.attr_rooms'),
    !o.property_type && t('objects.attr_type'),
  ].filter((x): x is string => typeof x === 'string')
  const attrsBlock = (
    <section className="block">
      <h2>{t('objects.detail_attrs')}</h2>
      {attrPresent.length > 0 && (
        <dl className="kv">
          {attrPresent.map(([label, value]) => (
            <Fragment key={label}>
              <dt>{label}</dt>
              <dd className="num">{value}</dd>
            </Fragment>
          ))}
        </dl>
      )}
      {attrMissing.length > 0 && (
        <p className="not-specified">{t('objects.not_specified', { attrs: attrMissing.join(', ') })}</p>
      )}
    </section>
  )

  // --- Блок «Расположение» -----------------------------------------------
  const locationBlock = (
    <section className="block">
      <h2>{t('objects.detail_location')}</h2>
      <dl className="kv">
        <dt>{t('objects.zone')}</dt>
        <dd>
          {o.zone_id ? (
            <a href={`#/zones/${o.zone_id}`}>{o.zone_name ?? t('objects.zone_unknown')}</a>
          ) : (
            (o.zone_name ?? t('objects.zone_unknown'))
          )}
        </dd>
      </dl>
      <Suspense fallback={null}>
        <MiniMap o={o} meta={meta} />
      </Suspense>
    </section>
  )

  // --- Блок «Продавец» (TZ_UI v5 U18, ТЗ v4 §6.4.7) -----------------------
  // Тип; название — для агентства или когда в данных только название
  // (тип null). Оба отсутствуют — честное «не указан».
  const sellerTypeText = o.seller_type
    ? t(`objects.seller_${o.seller_type}`, { defaultValue: o.seller_type })
    : null
  const sellerNameText =
    o.seller_name && (o.seller_type === 'agency' || o.seller_type == null) ? o.seller_name : null
  const sellerTextFull =
    sellerTypeText && sellerNameText
      ? `${sellerTypeText}, ${sellerNameText}`
      : (sellerTypeText ?? sellerNameText ?? null)
  const sellerBlock = (
    <section className="block">
      <h2>{t('objects.detail_seller')}</h2>
      {sellerTextFull ? (
        <p className="seller-line">{sellerTextFull}</p>
      ) : (
        <p className="muted">{t('objects.seller_unknown')}</p>
      )}
    </section>
  )

  // --- Блок «Объявление на площадке» --------------------------------------
  const listings = o.listings ?? []
  const listingBlock = (
    <section className="block">
      <h2>{t('objects.detail_listing')}</h2>
      {listings.length === 0 ? (
        <p className="muted">{t('objects.listing_none')}</p>
      ) : (
        listings.map((l) =>
          l.source_url ? (
            <a
              key={`${l.source_id}:${l.external_id}`}
              className="link-btn"
              href={l.source_url}
              target="_blank"
              rel="noopener noreferrer"
            >
              {t('objects.listing_open')}
              <span className="dom">{hostOf(l.source_url) ?? l.source_name}</span>
            </a>
          ) : (
            <div
              key={`${l.source_id}:${l.external_id}`}
              className="link-btn"
              aria-disabled="true"
            >
              {l.source_name} — {l.external_id}
            </div>
          ),
        )
      )}
    </section>
  )

  // --- Блок «Служебные сведения» ------------------------------------------
  const firstListing = listings[0]
  const serviceBlock = (
    <section className="block">
      <h2>{t('objects.detail_service')}</h2>
      <details className="service">
        {/* Summary — действие («Показать…»), а не повтор заголовка блока
            (критика Этапа 5) */}
        <summary>{t('objects.service_show')}</summary>
        <dl className="kv">
          <dt>{t('objects.svc_id')}</dt>
          <dd className="num">{o.id}</dd>
          <dt>{t('objects.svc_ext_id')}</dt>
          <dd className="num">{firstListing?.external_id ?? '—'}</dd>
          <dt>{t('objects.svc_first_seen')}</dt>
          <dd className="num">{formatDate(o.first_seen_at, locale)}</dd>
          <dt>{t('objects.svc_last_seen')}</dt>
          <dd className="num">{formatDate(o.last_seen_at, locale)}</dd>
          <dt>{t('objects.svc_source')}</dt>
          <dd>{firstListing?.source_name ?? '—'}</dd>
          <dt>{t('objects.svc_currency')}</dt>
          <dd className="num">{o.currency ?? '—'}</dd>
        </dl>
      </details>
    </section>
  )

  // --- Фид фото: первый блок экрана (TZ_UI v5 U17). ------------------------
  // Блока нет, когда фото нет — «нет фото» как состояние не вводится
  // (экран объясняется объектом, а не отсутствием контента).
  const photoFeedBlock =
    photos.length > 0 ? (
      <section className="block photo-block">
        <h2>{t('objects.detail_photos')}</h2>
        <div className="photo-feed" ref={feedRef} onScroll={onFeedScroll}>
          {photos.map((p, i) => (
            <button
              type="button"
              className="photo"
              key={p.ord}
              onClick={() => openViewer(i)}
              aria-label={t('objects.photo_alt', { n: i + 1 })}
            >
              <img src={photoSrc(p)} alt="" loading={i < 2 ? 'eager' : 'lazy'} />
            </button>
          ))}
        </div>
        <p className="photo-count num">
          {t('objects.photo_count', { cur: feedIdx + 1, total: photos.length })}
        </p>
      </section>
    ) : null

  // --- Полноэкранный просмотр (U07 Dialog: Escape, возврат фокуса). -------
  // Закрытие: кнопка, Escape, тап по фону (letterbox); листание — свайп
  // (scroll-snap того же фида).
  const viewer =
    viewerOpen && photos.length > 0 ? (
      <Dialog open label={t('objects.detail_photos')} onClose={closeViewer} className="viewer">
        <div
          className="viewer-root"
          onClick={(e) => {
            if (!(e.target as HTMLElement).closest('img, button')) closeViewer()
          }}
        >
          <button type="button" className="viewer-close" onClick={closeViewer}>
            {t('common.close')}
          </button>
          <div className="viewer-feed" ref={viewerFeedRef} onScroll={onViewScroll}>
            {photos.map((p, i) => (
              <div className="viewer-slide" key={p.ord}>
                <img src={photoSrc(p)} alt={t('objects.photo_alt', { n: i + 1 })} />
              </div>
            ))}
          </div>
          <p className="viewer-count num">
            {t('objects.photo_count', { cur: viewerIdx + 1, total: photos.length })}
          </p>
        </div>
      </Dialog>
    ) : null

  return (
    <>
      <header className="obj-head">
        {/* TZ_UI v5 U19: «← Объекты» — как на экране децилей */}
        <a className="back-link" href="#/list">
          ← {t('objects.back_objects')}
        </a>
        <h1 className="obj-addr">{o.title || o.address || t('objects.untitled', { id: o.id })}</h1>
        {o.title && o.address ? <p className="obj-sub">{o.address}</p> : null}
        <p className="obj-sub">
          {t('objects.deal', {
            country: t(`countries.${o.country}`, { defaultValue: o.country }),
            deal: t(`search.deal_${o.deal_type}`, { defaultValue: o.deal_type }),
          })}
        </p>
      </header>
      {/* Порядок блоков — ТЗ v4 §6.4: фото, оценка, цена (+ цена/м²),
          уход с рынка, характеристики, расположение, продавец, объявление,
          служебные сведения. */}
      {photoFeedBlock}
      {valuationBlock}
      {priceBlock}
      {hazardBlock}
      {attrsBlock}
      {o.description_original ? (
        <section className="block">
          <h2>{t('objects.detail_description')}</h2>
          <p className="desc" style={{ whiteSpace: 'pre-wrap' }}>
            {o.description_original}
          </p>
        </section>
      ) : null}
      {locationBlock}
      {sellerBlock}
      {listingBlock}
      {serviceBlock}
      {viewer}
    </>
  )
}
