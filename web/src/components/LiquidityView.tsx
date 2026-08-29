import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  getLiquidity,
  type LiquidityCalibDecile,
  type LiquidityModel,
  type Meta,
} from '../api'
import { formatDate, formatDateTime } from '../format'

interface Props {
  meta: Meta
}

// SVG калибровочной кривой (ТЗ §9.4): децили предсказанной
// вероятности (x) против фактической доли ушедших (y); диагональ —
// идеальная калибровка. Без графических библиотек (зависимостей в
// web/ на это не добавлялось).
function CalibrationCurve({ points }: { points: LiquidityCalibDecile[] }) {
  const { t } = useTranslation()
  const W = 380
  const H = 330
  const P = 46
  const sx = (v: number) => P + v * (W - 2 * P)
  const sy = (v: number) => H - P - v * (H - 2 * P)
  const poly = points.map((p) => `${sx(p.predicted).toFixed(1)},${sy(p.actual).toFixed(1)}`).join(' ')
  return (
    <svg
      viewBox={`0 0 ${W} ${H}`}
      className="calib-svg"
      role="img"
      aria-label={t('liquidity.calibration_curve')}
    >
      {/* сетка 0.25 */}
      {[0.25, 0.5, 0.75].map((g) => (
        <g key={g} className="calib-grid">
          <line x1={sx(g)} y1={sy(0)} x2={sx(g)} y2={sy(1)} />
          <line x1={sx(0)} y1={sy(g)} x2={sx(1)} y2={sy(g)} />
        </g>
      ))}
      {/* оси */}
      <line className="calib-axis" x1={sx(0)} y1={sy(0)} x2={sx(1)} y2={sy(0)} />
      <line className="calib-axis" x1={sx(0)} y1={sy(0)} x2={sx(0)} y2={sy(1)} />
      {/* диагональ идеальной калибровки */}
      <line className="calib-diag" x1={sx(0)} y1={sy(0)} x2={sx(1)} y2={sy(1)} />
      {/* кривая по децилям */}
      <polyline className="calib-curve" points={poly} fill="none" />
      {points.map((p) => (
        <circle
          key={p.decile}
          className="calib-dot"
          cx={sx(p.predicted)}
          cy={sy(p.actual)}
          r={4}
        >
          <title>
            {t('liquidity.decile_tooltip', {
              decile: p.decile,
              p: p.predicted.toFixed(3),
              a: p.actual.toFixed(3),
              n: p.n,
            })}
          </title>
        </circle>
      ))}
      {[0, 0.5, 1].map((v) => (
        <text key={`x${v}`} className="calib-label" x={sx(v)} y={sy(0) + 16} textAnchor="middle">
          {v}
        </text>
      ))}
      {[0, 0.5, 1].map((v) => (
        <text key={`y${v}`} className="calib-label" x={sx(0) - 8} y={sy(v) + 4} textAnchor="end">
          {v}
        </text>
      ))}
      <text className="calib-label" x={(W + P) / 2} y={H - 6} textAnchor="middle">
        {t('liquidity.x_axis')}
      </text>
      <text
        className="calib-label"
        x={12}
        y={(H - P) / 2}
        textAnchor="middle"
        transform={`rotate(-90 12 ${(H - P) / 2})`}
      >
        {t('liquidity.y_axis')}
      </text>
    </svg>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="stat">
      <div className="stat-label">{label}</div>
      <div className="stat-value">{value}</div>
    </div>
  )
}

export default function LiquidityView({ meta }: Props) {
  const { t, i18n } = useTranslation()
  const locale = i18n.language
  const [country, setCountry] = useState('')
  const [dealType, setDealType] = useState('')
  const [model, setModel] = useState<LiquidityModel | null | undefined>(undefined)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    getLiquidity({
      country: country || undefined,
      deal_type: dealType || undefined,
    })
      .then((d) => {
        if (!cancelled) {
          setModel(d.model)
          setError(null)
        }
      })
      .catch((e) => {
        if (!cancelled) {
          setError(String(e))
          setModel(null)
        }
      })
    return () => {
      cancelled = true
    }
  }, [country, dealType])

  const num = (v: number | null, digits = 4) => (v == null ? '—' : v.toFixed(digits))

  return (
    <div className="card">
      <div className="toolbar">
        <select value={country} onChange={(e) => setCountry(e.target.value)}>
          <option value="">{t('liquidity.all_countries')}</option>
          {meta.countries.map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </select>
        <select value={dealType} onChange={(e) => setDealType(e.target.value)}>
          <option value="">{t('liquidity.all_deal_types')}</option>
          {meta.deal_types.map((d) => (
            <option key={d} value={d}>
              {t(`search.deal_${d}`, { defaultValue: d })}
            </option>
          ))}
        </select>
      </div>

      {error && <div className="error">{error}</div>}
      {model === undefined && !error && <div>{t('common.loading')}</div>}
      {model === null && !error && <p className="muted">{t('liquidity.no_model')}</p>}

      {model && (
        <>
          <p>
            <span
              className={
                model.status === 'published'
                  ? 'tag'
                  : model.status === 'uncalibrated'
                    ? 'tag warn'
                    : 'tag off'
              }
            >
              {t(`liquidity.status_${model.status}`, { defaultValue: model.status })}
            </span>{' '}
            {model.reject_reason && <span className="muted">{model.reject_reason}</span>}
          </p>

          <div className="stat-grid">
            <Stat label={t('liquidity.market')} value={`${model.country} · ${model.deal_type}`} />
            <Stat label={t('liquidity.version')} value={model.model_version} />
            <Stat label={t('liquidity.computed_at')} value={formatDateTime(model.computed_at, locale)} />
            <Stat label={t('liquidity.horizon')} value={String(model.horizon_days)} />
            <Stat
              label={t('liquidity.completed_events')}
              value={`${model.n_completed_events} / ${model.min_events}`}
            />
            <Stat label={t('liquidity.person_periods')} value={String(model.n_person_periods)} />
            <Stat label={t('liquidity.params')} value={num(model.n_params, 0)} />
            <Stat label={t('liquidity.cutoff')} value={formatDate(model.train_cutoff_at, locale)} />
            <Stat label={t('liquidity.train')} value={String(model.n_train)} />
            <Stat label={t('liquidity.test')} value={String(model.n_test)} />
            <Stat label={t('liquidity.max_calib_dev')} value={num(model.max_calib_dev)} />
            <Stat label={t('liquidity.brier')} value={num(model.brier_score)} />
            <Stat label={t('liquidity.c_index')} value={num(model.c_index)} />
          </div>

          {model.brier_decomp && (
            <p className="muted">
              {t('liquidity.brier_reliability')}: {num(model.brier_decomp.reliability)} ·{' '}
              {t('liquidity.brier_resolution')}: {num(model.brier_decomp.resolution)} ·{' '}
              {t('liquidity.brier_uncertainty')}: {num(model.brier_decomp.uncertainty)}
            </p>
          )}

          {model.calibration && model.calibration.length > 0 ? (
            <div>
              <h3>{t('liquidity.calibration_curve')}</h3>
              <CalibrationCurve points={model.calibration} />
            </div>
          ) : null}

          <p className="muted">{t('liquidity.note')}</p>
        </>
      )}
    </div>
  )
}
