import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { getZone, type Meta, type ZoneCard } from '../api'
import { formatMoney, formatPercent } from '../format'

interface Props {
  id: number
  meta: Meta
}

export default function ZoneView({ id, meta }: Props) {
  const { t, i18n } = useTranslation()
  const locale = i18n.language
  const [data, setData] = useState<ZoneCard | null>(null)
  const [error, setError] = useState<string | null>(null)
  const exp = (c: string | null) => meta.currencies.find((x) => x.code === c)?.exponent ?? 2

  useEffect(() => {
    let cancelled = false
    getZone(id)
      .then((d) => {
        if (!cancelled) {
          setData(d)
          setError(null)
        }
      })
      .catch((e) => {
        if (!cancelled) setError(String(e))
      })
    return () => {
      cancelled = true
    }
  }, [id])

  if (error) return <div className="error">{t('common.error', { message: error })}</div>
  if (!data) return <div>{t('common.loading')}</div>

  const z = data.zone
  const cur = data.currency
  const money = (n: number | null) =>
    n != null && cur ? formatMoney(n, cur, exp(cur), locale) : t('map.no_value')
  const enough = data.n_objects >= data.zone_color_min_n

  return (
    <article className="zone-card">
      <header className="head">
        <p className="muted">
          <a href="#/">{t('map.tab')}</a>
        </p>
        <h1>
          {t('zones.district', { name: z.name })}
        </h1>
        <p className="muted">
          {t(`zones.level_${z.level}`, { defaultValue: z.level })}
          {z.parent_name ? ` · ${z.parent_name}` : ''}
          {` · ${z.source}`}
        </p>
      </header>
      <dl className="kv">
        <dt>{t('zones.n_objects')}</dt>
        <dd className="num">{data.n_objects.toLocaleString(locale)}</dd>
        <dt>{t('zones.median')}</dt>
        <dd className="num">{money(data.median_price_minor)}</dd>
        <dt>{t('zones.avg')}</dt>
        <dd className="num">{money(data.avg_price_minor)}</dd>
        <dt>{t('zones.ppsm')}</dt>
        <dd className="num">
          {data.avg_ppsm_minor != null && cur
            ? `${formatMoney(data.avg_ppsm_minor, cur, exp(cur), locale)} / ${t('search.sqm')}`
            : t('map.no_value')}
        </dd>
        <dt>{t('zones.trend')}</dt>
        <dd>
          {data.trend_down_share != null
            ? t('zones.trend_down', { pct: formatPercent(data.trend_down_share, locale), n: data.trend_n })
            : t(`zones.trend_${data.trend_reason ?? 'missing'}`)}
        </dd>
      </dl>
      {!enough && (
        <p className="dev-reason">{t('zones.insufficient', { n: data.zone_color_min_n, have: data.n_objects })}</p>
      )}
    </article>
  )
}
