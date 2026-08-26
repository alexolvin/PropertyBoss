import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { getMeta, type Meta } from './api'
import { LOCALES, setLocale, type Locale } from './i18n'
import SearchConfigsView from './components/SearchConfigsView'
import ObjectsView from './components/ObjectsView'

type Tab = 'search' | 'objects'

export default function App() {
  const { t, i18n } = useTranslation()
  const [meta, setMeta] = useState<Meta | null>(null)
  const [tab, setTab] = useState<Tab>('search')
  const [displayCurrency, setDisplayCurrency] = useState<string>(
    () => localStorage.getItem('pb.displayCurrency') ?? '',
  )
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    getMeta().then(setMeta).catch((e) => setError(String(e)))
  }, [])

  const changeDisplayCurrency = (code: string) => {
    if (code) localStorage.setItem('pb.displayCurrency', code)
    else localStorage.removeItem('pb.displayCurrency')
    setDisplayCurrency(code)
  }

  if (error) {
    return (
      <div className="error">
        {t('common.error', { message: error })}
      </div>
    )
  }
  if (!meta) return <div>{t('common.loading')}</div>

  return (
    <>
      <header>
        <h1>{t('app.title')}</h1>
        <nav>
          <button
            className={tab === 'search' ? 'active' : ''}
            onClick={() => setTab('search')}
          >
            {t('app.tab_search')}
          </button>
          <button
            className={tab === 'objects' ? 'active' : ''}
            onClick={() => setTab('objects')}
          >
            {t('app.tab_objects')}
          </button>
        </nav>
        <div className="spacer" />
        <label>
          {t('settings.language')}{' '}
          <select
            value={i18n.language}
            onChange={(e) => setLocale(e.target.value as Locale)}
          >
            {LOCALES.map((l) => (
              <option key={l} value={l}>
                {l}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t('settings.display_currency')}{' '}
          <select value={displayCurrency} onChange={(e) => changeDisplayCurrency(e.target.value)}>
            <option value="">—</option>
            {meta.currencies.map((c) => (
              <option key={c.code} value={c.code}>
                {c.code}
              </option>
            ))}
          </select>
        </label>
      </header>
      <main>
        {tab === 'search' ? (
          <SearchConfigsView meta={meta} />
        ) : (
          <ObjectsView meta={meta} displayCurrency={displayCurrency} />
        )}
      </main>
    </>
  )
}
