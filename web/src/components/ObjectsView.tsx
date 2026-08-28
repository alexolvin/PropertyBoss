import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { listObjects, type Meta, type ObjectsPage } from '../api'
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
