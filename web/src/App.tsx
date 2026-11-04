import { lazy, Suspense, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ApiError, getMeta, type Meta } from './api'
import ObjectsView from './components/ObjectsView'
import ObjectDetailView from './components/ObjectDetailView'
import ModelsView from './components/ModelsView'
import CalibrationView from './components/CalibrationView'
import SettingsView from './components/SettingsView'
import ZoneView from './components/ZoneView'

const MapView = lazy(() => import('./components/MapView'))

// Hash-маршруты (TZ_UI v3 §6): #/ — список объектов, #/objects/{id} — объект,
// #/models — «Модели», #/models/calibration/{страна}/{сделка} — числовые
// значения децилей кривой (отдельный экран, §6.5), #/settings — «Настройки».
// Внешний роутер не нужен: hash-ссылки дают системную кнопку «назад»,
// клавиатурную доступность и прямые ссылки без серверного маршрутизатора.
type Route =
  | { name: 'map'; highlight?: number }
  | { name: 'list' }
  | { name: 'object'; id: number }
  | { name: 'models' }
  | { name: 'calibration'; country: string; dealType: string }
  | { name: 'settings' }
  | { name: 'zone'; id: number }

function parseRoute(): Route {
  const h = window.location.hash.replace(/^#/, '')
  const m = h.match(/^\/objects\/(\d+)$/)
  if (m) return { name: 'object', id: Number(m[1]) }
  const zm = h.match(/^\/zones\/(\d+)$/)
  if (zm) return { name: 'zone', id: Number(zm[1]) }
  const cm = h.match(/^\/models\/calibration\/([A-Z]{2})\/(sale|rent)$/)
  if (cm) return { name: 'calibration', country: cm[1], dealType: cm[2] }
  if (h === '/list' || h === '/objects') return { name: 'list' }
  if (h === '/models') return { name: 'models' }
  if (h === '/settings') return { name: 'settings' }
  const hm = h.match(/^\/map\/(\d+)$/)
  if (hm) return { name: 'map', highlight: Number(hm[1]) }
  return { name: 'map' }
}

function useRoute(): Route {
  const [route, setRoute] = useState<Route>(parseRoute)
  useEffect(() => {
    const onHash = () => {
      setRoute(parseRoute())
      // Смена экрана — чтение сверху, как в макетах (TZ_UI v3 §6).
      window.scrollTo(0, 0)
    }
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [])
  return route
}

export default function App() {
  const { t } = useTranslation()
  const route = useRoute()
  const [meta, setMeta] = useState<Meta | null>(null)
  const [displayCurrency, setDisplayCurrency] = useState<string>(
    () => localStorage.getItem('pb.displayCurrency') ?? '',
  )
  const [error, setError] = useState<ApiError | null>(null)
  // TZ UI v5 U09: скелетон при старте — только если /api/meta дольше 300 мс.
  const [metaLoading, setMetaLoading] = useState(true)
  const [metaSlow, setMetaSlow] = useState(false)
  const [reload, setReload] = useState(0)

  useEffect(() => {
    let cancelled = false
    setMetaLoading(true)
    setMetaSlow(false)
    const timer = setTimeout(() => {
      if (!cancelled) setMetaSlow(true)
    }, 300)
    getMeta()
      .then((d) => {
        if (!cancelled) {
          setMeta(d)
          setError(null)
        }
      })
      .catch((e) => {
        if (!cancelled) setError(e instanceof ApiError ? e : new ApiError('network', String(e)))
      })
      .finally(() => {
        if (!cancelled) {
          setMetaLoading(false)
          setMetaSlow(false)
        }
      })
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [reload])

  const changeDisplayCurrency = (code: string) => {
    if (code) localStorage.setItem('pb.displayCurrency', code)
    else localStorage.removeItem('pb.displayCurrency')
    setDisplayCurrency(code)
  }

  // TZ UI v5 U09: мета не загрузилась — ошибка с кнопкой «Повторить»
  // внутри <main> (тестовый инвариант main > .error).
  if (error) {
    return (
      <main id="main">
        <div className="error">
          {error.kind === 'offline'
            ? t('common.offline')
            : t('common.error', { message: error.message })}
        </div>
        <button className="ghost retry-btn" onClick={() => setReload((r) => r + 1)}>
          {t('common.retry')}
        </button>
      </main>
    )
  }
  // TZ UI v5 U09: запрос > 300 мс — скелетон-список, не текст «Загрузка…».
  if (!meta) {
    return metaLoading && metaSlow ? (
      <main id="main">
        <div className="skeleton-list" aria-hidden="true">
          <div className="skeleton-row" />
          <div className="skeleton-row" />
          <div className="skeleton-row" />
        </div>
      </main>
    ) : null
  }

  // Экран объекта считается разделом «Объекты», экран децилей —
  // разделом «Модели» для подсветки вкладки.
  const section =
    route.name === 'object' || route.name === 'list'
      ? 'list'
      : route.name === 'calibration'
        ? 'models'
        : route.name === 'zone'
          ? 'map'
          : route.name

  return (
    <>
      <main id="main" className={route.name === 'map' ? 'map-main' : undefined}>
        {route.name === 'map' && (
          <Suspense fallback={<div className="skeleton-block" aria-hidden="true" />}>
            <MapView
              meta={meta}
              displayCurrency={displayCurrency}
              highlightId={route.highlight}
            />
          </Suspense>
        )}
        {route.name === 'list' && (
          <ObjectsView meta={meta} displayCurrency={displayCurrency} />
        )}
        {route.name === 'object' && (
          <ObjectDetailView
            id={route.id}
            meta={meta}
            displayCurrency={displayCurrency}
          />
        )}
        {route.name === 'zone' && <ZoneView id={route.id} meta={meta} />}
        {route.name === 'models' && <ModelsView />}
        {route.name === 'calibration' && (
          <CalibrationView country={route.country} dealType={route.dealType} />
        )}
        {route.name === 'settings' && (
          <SettingsView
            meta={meta}
            displayCurrency={displayCurrency}
            onDisplayCurrency={changeDisplayCurrency}
          />
        )}
      </main>
      <nav className="tabbar" aria-label={t('app.title')}>
        <a
          href="#/"
          className={section === 'map' ? 'active' : ''}
          aria-current={section === 'map' ? 'page' : undefined}
        >
          {t('map.tab')}
        </a>
        <a
          href="#/list"
          className={section === 'list' ? 'active' : ''}
          aria-current={section === 'list' ? 'page' : undefined}
        >
          {t('app.tab_objects')}
        </a>
        <a
          href="#/models"
          className={section === 'models' ? 'active' : ''}
          aria-current={section === 'models' ? 'page' : undefined}
        >
          {t('app.tab_models')}
        </a>
        <a
          href="#/settings"
          className={section === 'settings' ? 'active' : ''}
          aria-current={section === 'settings' ? 'page' : undefined}
        >
          {t('app.tab_settings')}
        </a>
      </nav>
    </>
  )
}
