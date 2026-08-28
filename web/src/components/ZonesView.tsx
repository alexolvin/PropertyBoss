import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { listZones, type Meta, type ZonesPage } from '../api'

interface Props {
  meta: Meta
}

const PER_PAGE = 50

// Зоны (этап 4, ТЗ §7.1): список полигонов с иерархией и атрибуцией
// источников данных (ТЗ §13 — «Источник данных» виден пользователю).
export default function ZonesView({ meta }: Props) {
  const { t } = useTranslation()
  const [country, setCountry] = useState('')
  const [level, setLevel] = useState('')
  const [page, setPage] = useState(1)
  const [data, setData] = useState<ZonesPage | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    listZones({
      country: country || undefined,
      level: level || undefined,
      page,
      per_page: PER_PAGE,
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
  }, [country, level, page])

  const pages = data ? Math.max(1, Math.ceil(data.total / data.per_page)) : 1

  return (
    <div className="card">
      <div className="toolbar">
        <select
          value={country}
          onChange={(e) => {
            setCountry(e.target.value)
            setPage(1)
          }}
        >
          <option value="">{t('zones.all_countries')}</option>
          {meta.countries.map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </select>
        <select
          value={level}
          onChange={(e) => {
            setLevel(e.target.value)
            setPage(1)
          }}
        >
          <option value="">{t('zones.all_levels')}</option>
          <option value="region">{t('zones.level_region')}</option>
          <option value="municipality">{t('zones.level_municipality')}</option>
          <option value="zone">{t('zones.level_zone')}</option>
        </select>
        {data && (
          <span className="muted">
            {t('zones.total', { count: data.total })} — {t('objects.page')} {page}{' '}
            {t('objects.of')} {pages}
          </span>
        )}
      </div>

      {error && <div className="error">{error}</div>}
      {!data && !error && <div>{t('common.loading')}</div>}
      {data && data.zones.length === 0 && <p className="muted">{t('zones.no_zones')}</p>}
      {data && data.zones.length > 0 && (
        <>
          <table>
            <thead>
              <tr>
                <th>#</th>
                <th>{t('search.country')}</th>
                <th>{t('zones.level_col')}</th>
                <th>{t('zones.name')}</th>
                <th>{t('zones.code')}</th>
                <th>{t('zones.parent')}</th>
                <th>{t('zones.source')}</th>
              </tr>
            </thead>
            <tbody>
              {data.zones.map((z) => (
                <tr key={z.id}>
                  <td>{z.id}</td>
                  <td>{z.country}</td>
                  <td>{t(`zones.level_${z.level}`)}</td>
                  <td>{z.name}</td>
                  <td>{z.external_code ?? '—'}</td>
                  <td>{z.parent_name ?? '—'}</td>
                  <td>{z.source}</td>
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
      {data && data.sources.length > 0 && (
        <p className="muted" style={{ marginTop: 8 }}>
          {t('zones.sources_attribution', { sources: data.sources.join(', ') })}
        </p>
      )}
    </div>
  )
}
