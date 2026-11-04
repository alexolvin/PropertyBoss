import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { LiquidityCalibDecile } from '../api'

// SVG калибровочной кривой (ТЗ §9.4): децили предсказанной
// вероятности (x) против фактической доли ушедших (y); диагональ —
// идеальная калибровка. Без графических библиотек.
//
// TZ_UI v5 U20/U21: значение точки доступно ТАПОМ — невидимая зона
// r≈12 (viewBox-единицы) поверх каждой точки; выбранный дециль —
// строкой под графиком. <title> сохранён для hover на десктопе;
// полные числовые значения — на экране децилей (TZ_UI v3 §6.5).
// Зоны НЕ имеют role/tabindex: они не «кнопки» для assertTargetSizes —
// путь с полным значением и клавиатурой — экран децилей.
export default function CalibrationCurve({ points }: { points: LiquidityCalibDecile[] }) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState<LiquidityCalibDecile | null>(null)
  const W = 380
  const H = 330
  const P = 46
  const sx = (v: number) => P + v * (W - 2 * P)
  const sy = (v: number) => H - P - v * (H - 2 * P)
  const poly = points
    .map((p) => `${sx(p.predicted).toFixed(1)},${sy(p.actual).toFixed(1)}`)
    .join(' ')
  const row = (p: LiquidityCalibDecile) =>
    t('models.decile_row', {
      p: p.predicted.toFixed(3),
      a: p.actual.toFixed(3),
      n: p.n,
    })
  const tip = (p: LiquidityCalibDecile) =>
    t('models.decile_tooltip', { decile: p.decile, row: row(p) })

  return (
    <>
      <div className="chart-wrap">
        <svg
          viewBox={`0 0 ${W} ${H}`}
          className="calib-svg"
          role="img"
          aria-label={t('models.calib_curve_title')}
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
          <polyline className="calib-curve" points={poly} fill="none" />
          {points.map((p) => (
            <g key={p.decile}>
              <circle className="calib-dot" cx={sx(p.predicted)} cy={sy(p.actual)} r={4}>
                <title>{tip(p)}</title>
              </circle>
              {/* Тап-зона: невидимая, выше точки по Z-порядку; переключение
                  при повторном тапе */}
              <circle
                className="calib-hit"
                cx={sx(p.predicted)}
                cy={sy(p.actual)}
                r={12}
                onClick={() => setSelected(selected === p ? null : p)}
              />
            </g>
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
            {t('models.x_axis')}
          </text>
          <text
            className="calib-label"
            x={12}
            y={(H - P) / 2}
            textAnchor="middle"
            transform={`rotate(-90 12 ${(H - P) / 2})`}
          >
            {t('models.y_axis')}
          </text>
        </svg>
      </div>
      {/* Выбранная точка — значение строкой под графиком (U20) */}
      {selected && (
        <p className="calib-tip num" role="status">
          {tip(selected)}
        </p>
      )}
    </>
  )
}
