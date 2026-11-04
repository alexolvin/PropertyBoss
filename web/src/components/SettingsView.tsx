import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { listZones, type Meta, type ZonesPage } from '../api'
import { LOCALES, setLocale, type Locale } from '../i18n'
import SearchConfigsView from './SearchConfigsView'

// ISO (UTC) → "2026-09-10 01:36 UTC": время сборки — факт сборки,
// показываем без пересчёта в локаль зрителя (иначе время врёт).
function buildTime(iso: string): string {
  return iso.slice(0, 16).replace('T', ' ') + ' UTC'
}

interface Props {
  meta: Meta
  displayCurrency: string
  onDisplayCurrency: (c: string) => void
}

// Поиск по зонам (TZ_UI v3 §6.6): ввод с паузой, выдача до 20,
// без страниц. Запрос не уходит, пока запрос короче двух символов.
const MIN_QUERY = 2
const DEBOUNCE_MS = 300

export default function SettingsView({ meta, displayCurrency, onDisplayCurrency }: Props) {
  const { t, i18n } = useTranslation()
  const [query, setQuery] = useState('')
  const [zones, setZones] = useState<ZonesPage | null>(null)
  const [searching, setSearching] = useState(false)

  const trimmed = query.trim()
  const active = trimmed.length >= MIN_QUERY

  useEffect(() => {
    if (!active) {
      setZones(null)
      return
    }
    let cancelled = false
    setSearching(true)
    const timer = setTimeout(() => {
      listZones({ q: trimmed })
        .then((d) => {
          if (!cancelled) {
            setZones(d)
            setSearching(false)
          }
        })
        .catch(() => {
          if (!cancelled) {
            setZones(null)
            setSearching(false)
          }
        })
    }, DEBOUNCE_MS)
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [trimmed, active])

  // Валюты рынка по всем странам (примечание под селектом).
  const marketCurrencies = [...new Set(Object.values(meta.market_currencies))].join(', ')

  return (
    <>
      <header className="head">
        <h1>{t('settings.title')}</h1>
      </header>

      <section className="settings-block">
        <h2>{t('settings.search_configs')}</h2>
        <SearchConfigsView meta={meta} />
      </section>

      <section className="settings-block">
        <h2>{t('settings.zone_search')}</h2>
        {/* TZ_UI v5 U23: поиск — со спиннером в поле (aria-busy),
            не строкой «Загрузка…» под полем */}
        <div
          className={'zone-search' + (active && searching ? ' busy' : '')}
          aria-busy={active && searching || undefined}
        >
          <input
            type="search"
            value={query}
            placeholder={t('settings.zone_search_ph')}
            onChange={(e) => setQuery(e.target.value)}
            aria-label={t('settings.zone_search')}
          />
        </div>
        {active && !searching && zones && (
          <>
            <p className="muted num">
              {t('settings.zone_search_found', { count: zones.zones.length })}
            </p>
            {zones.zones.length === 0 && <p className="muted">{t('settings.zone_search_none')}</p>}
            {zones.zones.map((z) => (
              <div className="zone-result" key={z.id}>
                <div className="zone-name">{z.name}</div>
                <div className="zone-meta">
                  {t(`zones.level_${z.level}`, { defaultValue: z.level })}
                  {z.parent_name && (
                    <div>
                      {t('zones.parent')}: {z.parent_name}
                    </div>
                  )}
                  {z.external_code && (
                    <div>
                      {t('zones.code')}: {z.external_code}
                    </div>
                  )}
                </div>
              </div>
            ))}
          </>
        )}
      </section>

      {/* Язык и валюта отображения — один блок «Оформление» (критика
          Этапа 5: две «главные» секции мелкой настройки не создают иерархии) */}
      <section className="settings-block">
        <h2>{t('settings.appearance')}</h2>
        <div className="pref-row">
          <label>
            {t('settings.language')}
            <select
              value={i18n.language}
              onChange={(e) => setLocale(e.target.value as Locale)}
              aria-label={t('settings.language')}
            >
              {LOCALES.map((l) => (
                <option key={l} value={l}>
                  {l.toUpperCase()}
                </option>
              ))}
            </select>
          </label>
        </div>
        <div className="pref-row">
          <label>
            {t('settings.display_currency')}
            <select
              value={displayCurrency}
              onChange={(e) => onDisplayCurrency(e.target.value)}
              aria-label={t('settings.display_currency')}
            >
              <option value="">{t('settings.market_only')}</option>
              {meta.currencies.map((c) => (
                <option key={c.code} value={c.code}>
                  {c.code}
                </option>
              ))}
            </select>
          </label>
        </div>
        {displayCurrency === '' && marketCurrencies && (
          <p className="muted">
            {t('settings.market_currency_note', { currency: marketCurrencies })}
          </p>
        )}
      </section>

      {/* Раздача (TZ UI v5 U02): «что я сейчас смотрю» — хэш коммита и
          время сборки из бандла; сверка с версией на сервере —
          рассхождение видно без обращения к файловой системе. */}
      <section className="settings-block">
        <h2>{t('settings.distribution_title')}</h2>
        <p className="muted num">
          {t('settings.build_line', {
            commit: __PB_BUILD__.commit,
            time: buildTime(__PB_BUILD__.builtAt),
          })}
        </p>
        {meta.ui_build && meta.ui_build.commit !== __PB_BUILD__.commit && (
          <p className="error">
            {t('settings.build_mismatch', {
              bundle: __PB_BUILD__.commit,
              server: meta.ui_build.commit,
            })}
          </p>
        )}
      </section>
    </>
  )
}
