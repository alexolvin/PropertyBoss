import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { listModels, type ModelsInfo } from '../api'

interface Props {
  country: string
  dealType: string
}

// Отдельный экран числовых значений децилей калибровочной кривой
// (TZ_UI v3 §6.5: «давать числовые значения на отдельном экране, а не в
// раскрывающемся элементе под ней»). Кривая как изображение — в разделе
// «Модели»; здесь — таблица десяти децилей.
export default function CalibrationView({ country, dealType }: Props) {
  const { t } = useTranslation()
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

  const pair = data.pairs.find((p) => p.country === country && p.deal_type === dealType)
  const liq = pair?.liquidity
  const calib = liq?.calibration

  return (
    <>
      <header className="head">
        {/* Явный выход на раздел: drill-down, а не самостоятельный экран
            (критика Этапа 5: «не было как вернуться») */}
        <a className="back-link" href="#/models">
          {t('models.back_to_models')}
        </a>
        <h1>
          {t(`countries.${country}`, { defaultValue: country })} —{' '}
          {t(`search.deal_${dealType}`, { defaultValue: dealType })}
        </h1>
        <p>{t('models.calib_values_title')}</p>
      </header>

      <section className="block">
        <div className="calib-card">
        {liq && (
          <p className="note">
            {t('objects.model_version', { version: liq.model_version })} —{' '}
            {t('models.calib_values_horizon', { horizon: liq.horizon_days })}
          </p>
        )}
        {calib && calib.length > 0 ? (
          <dl className="kv calib-kv">
            {calib.map((p) => (
              <div className="calib-row" key={p.decile}>
                <dt>{t('models.decile', { decile: p.decile })}</dt>
                <dd className="num">{t('models.decile_row', {
                  p: p.predicted.toFixed(3),
                  a: p.actual.toFixed(3),
                  n: p.n,
                })}</dd>
              </div>
            ))}
          </dl>
        ) : (
          <p className="note">{t('models.calib_values_absent')}</p>
        )}
        </div>
      </section>
    </>
  )
}
