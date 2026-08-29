import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { listObjects, type Meta, type ObjectItem, type ObjectsPage } from '../api'
import { formatDate, formatMoney } from '../format'

interface Props {
  meta: Meta
  displayCurrency: string
}

const PER_PAGE = 50

export default function ObjectsView({ meta, displayCurrency }: Props) {
  const { t, i18n } = useTranslation()
  const locale = i18n.language
  const [country, setCountry] = useState('')
  const [status, setStatus] = useState('')
  const [page, setPage] = useState(1)
  const [data, setData] = useState<ObjectsPage | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    listObjects({
      country: country || undefined,
      status: status || undefined,
      page,
      per_page: PER_PAGE,
      display_currency: displayCurrency || undefined,
    })
      .then((d) => {
        if (!cancelled) setData(d)
      })
      .catch((e) => {
        if (!cancelled) setError(String(e))
      })
    return () => {
      cancelled = true
    }
  }, [country, status, page, displayCurrency])

  const expOf = (code: string | null | undefined) =>
    meta.currencies.find((c) => c.code === code)?.exponent ?? 2

  // Оценка (ТЗ §7.3): число без интервала не показывается.
  // price_deviation = price/predicted − 1: >0 — дороже модели, <0 — дешевле.
  const renderValuation = (o: ObjectItem) => {
    const v = o.valuation
    if (!v) return <span className="muted">—</span>
    if (v.price_deviation == null) {
      // Причину переводим по префиксу до «:» (в хвосте — параметры прогона).
      const prefix = v.null_reason ? v.null_reason.split(':')[0].trim() : ''
      return (
        <span className="muted" title={v.null_reason ?? ''}>
          {t(`objects.valuation_reason_${prefix}`, { defaultValue: v.null_reason ?? '—' })}
        </span>
      )
    }
    if (v.interval_low_minor == null || v.interval_high_minor == null) {
      return <span className="muted">{t('objects.valuation_no_interval')}</span>
    }
    const title = t('objects.valuation_tooltip', {
      version: v.model_version ?? '—',
      n: v.sample_size,
      r2: v.r_squared != null ? v.r_squared.toFixed(2) : '—',
    })
    const sign = v.price_deviation >= 0 ? '+' : ''
    return (
      <span title={title}>
        {sign}
        {(v.price_deviation * 100).toFixed(1)}%{' '}
        <span className="muted">
          [
          {formatMoney(v.interval_low_minor, o.currency, expOf(o.currency), locale)}–
          {formatMoney(v.interval_high_minor, o.currency, expOf(o.currency), locale)}]
        </span>
        {v.zone_fallback ? (
          <span className="tag warn" title={t('objects.valuation_fallback')}>
            z↑
          </span>
        ) : null}
      </span>
    )
  }

  // Ликвидность (ТЗ §9.2–9.3): вероятность ухода с рынка, не продажи.
  // NULL с причиной, пока модель не опубликована.
  const renderHazard = (o: ObjectItem) => {
    const h = o.hazard
    if (!h) return <span className="muted">—</span>
    if (h.probability == null) {
      const prefix = h.null_reason ? h.null_reason.split(':')[0].trim() : ''
      return (
        <span className="muted" title={h.null_reason ?? ''}>
          {t(`objects.hazard_reason_${prefix}`, { defaultValue: h.null_reason ?? '—' })}
        </span>
      )
    }
    const title = t('objects.hazard_tooltip', {
      days: h.horizon_days ?? '—',
      version: h.model_version ?? '—',
      n: h.events_in_training,
    })
    return (
      <span title={title}>
        {(h.probability * 100).toFixed(1)}%
      </span>
    )
  }

  const pages = data ? Math.max(1, Math.ceil(data.total / data.per_page)) : 1

  return (
    <div className="card">
      <div className="toolbar">
        <select value={country} onChange={(e) => { setCountry(e.target.value); setPage(1) }}>
          <option value="">{t('objects.all_countries')}</option>
          {meta.countries.map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </select>
        <select value={status} onChange={(e) => { setStatus(e.target.value); setPage(1) }}>
          <option value="">{t('objects.all_status')}</option>
          <option value="active">{t('objects.status_active')}</option>
          <option value="delisted">{t('objects.status_delisted')}</option>
        </select>
        {data && (
          <span className="muted">
            {t('objects.page')} {page} {t('objects.of')} {pages}
          </span>
        )}
      </div>

      {error && <div className="error">{error}</div>}
      {!data && !error && <div>{t('common.loading')}</div>}
      {data && data.objects.length === 0 && (
        <p className="muted">{t('objects.no_objects')}</p>
      )}
      {data && data.objects.length > 0 && (
        <>
          <table>
            <thead>
              <tr>
                <th>#</th>
                <th>{t('objects.address')}</th>
                <th>{t('objects.zone')}</th>
                <th>{t('search.country')}</th>
                <th>{t('search.property_type')}</th>
                <th>{t('objects.area')}</th>
                <th>{t('objects.rooms')}</th>
                <th>{t('objects.price')}</th>
                {displayCurrency && <th>{t('objects.price_display')}</th>}
                <th>{t('objects.valuation')}</th>
                <th>{t('objects.hazard')}</th>
                <th>{t('objects.status')}</th>
                <th>{t('objects.first_seen')}</th>
                <th>{t('objects.last_seen')}</th>
              </tr>
            </thead>
            <tbody>
              {data.objects.map((o) => (
                <tr key={o.id}>
                  <td>{o.id}</td>
                  <td>{o.address ?? '—'}</td>
                  <td>
                    {o.zone_name ? (
                      <span title={o.zone_source ?? ''}>
                        {t(`zones.level_${o.zone_level ?? ''}`, {
                          defaultValue: o.zone_level ?? '',
                        })}
                        {' · '}
                        {o.zone_name}
                      </span>
                    ) : (
                      '—'
                    )}
                  </td>
                  <td>{o.country}</td>
                  <td>{o.property_type ?? '—'}</td>
                  <td>{o.area_sqm ?? '—'}</td>
                  <td>
                    {o.rooms === null ? '—' : `${o.rooms} ${t('objects.rooms_short')}`}
                  </td>
                  <td>
                    {formatMoney(o.price_minor, o.currency, expOf(o.currency), locale)}
                  </td>
                  {displayCurrency && (
                    <td>
                      {o.price_display ? (
                        <>
                          {formatMoney(
                            o.price_display.minor,
                            o.price_display.currency,
                            expOf(o.price_display.currency),
                            locale,
                          )}
                          {o.price_display.rate_stale ? (
                            <span
                              className="tag warn"
                              title={t('common.stale_rate', {
                                date: formatDate(o.price_display.rate_date, locale),
                              })}
                            >
                              ⚠
                            </span>
                          ) : (
                            <span
                              className="muted"
                              title={t('common.rate_date', {
                                date: formatDate(o.price_display.rate_date, locale),
                              })}
                            >
                              ·
                            </span>
                          )}
                        </>
                      ) : (
                        <span className="muted">—</span>
                      )}
                    </td>
                  )}
                  <td>{renderValuation(o)}</td>
                  <td>{renderHazard(o)}</td>
                  <td>
                    {o.status === 'active' ? (
                      <span className="tag">{t('objects.status_active')}</span>
                    ) : (
                      <span className="tag off">
                        {t('objects.status_delisted')}
                        {o.delisted_reason ? (
                          <span className="muted">
                            {' '}
                            ({t(`objects.reason_${o.delisted_reason}`, {
                              defaultValue: o.delisted_reason,
                            })})
                          </span>
                        ) : null}
                      </span>
                    )}
                  </td>
                  <td>{formatDate(o.first_seen_at, locale)}</td>
                  <td>{formatDate(o.last_seen_at, locale)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="pager" style={{ marginTop: 12 }}>
            <button className="ghost" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>
              ‹
            </button>
            <button className="ghost" disabled={page >= pages} onClick={() => setPage((p) => p + 1)}>
              ›
            </button>
          </div>
        </>
      )}
    </div>
  )
}
