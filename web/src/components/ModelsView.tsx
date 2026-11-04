import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { listModels, type ModelsInfo, type PairModels } from '../api'
import { formatDateTime, formatDate } from '../format'
import CalibrationCurve from './CalibrationCurve'

/**
 * Раздел «Модели» (TZ_UI v3 §6.5): состояние по парам «страна — тип
 * сделки», показатели качества, калибровочная кривая, источники и
 * последние сканирования. Пока оценок нет — раздел объясняет это
 * текстом, пустотами не (ТЗ §7.3).
 */
export default function ModelsView() {
  const { t, i18n } = useTranslation()
  const locale = i18n.language
  const [data, setData] = useState<ModelsInfo | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    listModels()
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
  }, [])

  if (error) return <div className="error">{t('common.error', { message: error })}</div>
  if (!data) return <div>{t('common.loading')}</div>

  const pairName = (c: string, d: string) =>
    `${t(`countries.${c}`, { defaultValue: c })} — ${t(`search.deal_${d}`, {
      defaultValue: d,
    })}`

  const hasEstimate = data.pairs.some(
    (p) => (p.deviation?.n_with_deviation ?? 0) > 0 || p.liquidity?.status === 'published',
  )

  const devText = (p: PairModels): string => {
    const d = p.deviation
    if (!d) return t('models.dev_none', { reason: t('models.dev_reason_unknown') })
    if (d.n_with_deviation > 0)
      return t('models.dev_some', { with: d.n_with_deviation, total: d.n_valued })
    const prefix = (d.dominant_reason ?? '').split(':')[0].trim()
    return t('models.dev_none', {
      reason: t(`models.dev_reason_${prefix}`, {
        defaultValue: t('models.dev_reason_unknown'),
      }),
    })
  }

  const liqText = (p: PairModels): string => {
    const l = p.liquidity
    if (!l) return t('models.liq_none')
    switch (l.status) {
      case 'published':
        return t('models.liq_published', {
          version: l.model_version,
          n: l.n_completed_events,
        })
      case 'uncalibrated':
        return t('models.liq_uncalibrated', { reason: l.reject_reason ?? '' })
      default:
        // «(объектов N)» убрано: число объектов уже в заголовке пары —
        // один факт, одна подача (критика Этапа 5)
        return t('models.liq_insufficient', {
          n: l.n_completed_events,
          min: l.min_events,
        })
    }
  }

  // Показатели качества: строки только по парам, где метрики есть.
  const qualityRows: { label: string; value: string }[] = []
  for (const p of data.pairs) {
    const name = pairName(p.country, p.deal_type)
    const d = p.deviation
    if (d?.r_squared != null)
      qualityRows.push({
        label: `${t('models.quality_r2')} — ${name}`,
        value: d.r_squared.toLocaleString(locale),
      })
    if (d?.sample_size)
      qualityRows.push({
        label: `${t('models.quality_sample')} — ${name}`,
        value: d.sample_size.toLocaleString(locale),
      })
    const l = p.liquidity
    if (l?.brier_score != null)
      qualityRows.push({
        label: `${t('models.quality_brier')} — ${name}`,
        value: l.brier_score.toLocaleString(locale),
      })
    if (l?.c_index != null)
      qualityRows.push({
        label: `${t('models.quality_cindex')} — ${name}`,
        value: l.c_index.toLocaleString(locale),
      })
  }

  const calibrated = data.pairs.filter(
    (p) => p.liquidity?.status === 'published' && (p.liquidity.calibration?.length ?? 0) > 0,
  )

  // Ключевой факт «идут ли сканирования» — в первом экране (критика
  // Этапа 5), а не только в блоке источников внизу.
  const lastScanSource = data.sources
    .filter((s) => s.last_complete_scan_at)
    .sort((a, b) => (a.last_complete_scan_at! < b.last_complete_scan_at! ? 1 : -1))[0]

  return (
    <>
      <header className="head">
        <h1>{t('models.title')}</h1>
        {/* Вердикт о состоянии системы — доминирующий элемент экрана,
            а не мелкая серая строка под заголовком (критика Этапа 5) */}
        {!hasEstimate && <p className="status-line">{t('models.intro_collecting')}</p>}
        {lastScanSource?.last_complete_scan_at && (
          <p className="status-scan num">
            {t('models.scan_line', {
              source: lastScanSource.name,
              date: formatDate(lastScanSource.last_complete_scan_at, locale),
            })}
          </p>
        )}
      </header>

      <section className="block">
        {/* Заголовок «Состояние моделей» убран: полный дубликат H1 +
            строки-вердикта (критика Этапа 5) */}
        {data.pairs.map((p) => (
          <div
            className={'pair' + (p.object_count === 0 ? ' empty' : ' live')}
            key={`${p.country}|${p.deal_type}`}
          >
            {/* Пара с данными — полный вес и жирное число; пустая пара —
                приглушена: это разные типы информации (критика Этапа 5) */}
            <div className="pair-name">
              {/* D2: разделитель — без него число приклеивалось к имени
                  («Чехия — продажа8 221») */}
              {pairName(p.country, p.deal_type)}{' '}
              <span className="pair-st num">
                {p.object_count > 0 ? (
                  <b>{p.object_count.toLocaleString(locale)}</b>
                ) : (
                  p.object_count.toLocaleString(locale)
                )}
              </span>
            </div>
            {p.object_count === 0 ? (
              <p className="muted">{t('models.no_objects')}</p>
            ) : (
              <>
                <p className="model-line">
                  <span className="model-label">{t('models.dev_label')}</span> {devText(p)}
                </p>
                <p className="model-line">
                  <span className="model-label">{t('models.liq_label')}</span> {liqText(p)}
                </p>
              </>
            )}
          </div>
        ))}
      </section>

      <section className="block">
        <h2>{t('models.quality_title')}</h2>
        {qualityRows.length === 0 ? (
          <p className="note">{t('models.quality_none')}</p>
        ) : (
          // TZ UI v5 U13 (E13): метки «R² — название модели» длиннее
          // колонки на узких экранах — сетка minmax(0, max-content)
          // сжимает метку, .stack переносит значение под метку.
          <dl className="kv stack">
            {qualityRows.map((r) => (
              <FragmentRow key={r.label} label={r.label} value={r.value} />
            ))}
          </dl>
        )}
      </section>

      <section className="block">
        <h2>{t('models.calib_title')}</h2>
        {calibrated.length === 0 ? (
          <p className="note">{t('models.calib_absent')}</p>
        ) : (
          calibrated.map((p) => (
            <div className="pair" key={`calib|${p.country}|${p.deal_type}`}>
              <div className="pair-name">{pairName(p.country, p.deal_type)}</div>
              {/* Кривая — изображение; числовые значения децилей — на
                  отдельном экране, не в раскрывающемся элементе (TZ_UI v3 §6.5) */}
              <CalibrationCurve points={p.liquidity!.calibration!} />
              <a
                className="calib-link"
                href={`#/models/calibration/${p.country}/${p.deal_type}`}
              >
                {t('models.calib_deciles_link')}
              </a>
            </div>
          ))
        )}
      </section>

      <section className="block">
        <h2>{t('models.sources_title')}</h2>
        {data.sources.map((s) => (
          <div className="pair" key={s.id}>
            <div className="pair-name">
              {/* D2: то же — статус приклеивался к имени источника */}
              {s.name}{' '}
              <span className="pair-st">
                {t(`models.state_${s.state}`, {
                  defaultValue: s.state === 'active' ? t('models.state_active') : s.state,
                })}
              </span>
            </div>
            <dl className="kv">
              <dt>{t('models.last_scan')}</dt>
              <dd className="num">
                {s.last_complete_scan_at
                  ? formatDateTime(s.last_complete_scan_at, locale)
                  : t('models.last_scan_none')}
              </dd>
            </dl>
          </div>
        ))}
      </section>
    </>
  )
}

function FragmentRow({ label, value }: { label: string; value: string }) {
  return (
    <>
      <dt>{label}</dt>
      <dd className="num">{value}</dd>
    </>
  )
}
