import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import maplibregl from 'maplibre-gl'
import 'maplibre-gl/dist/maplibre-gl.css'
import {
  getMapObjects,
  getObject,
  getZonesGeoJSON,
  listObjects,
  type DevClass,
  type MapArea,
  type MapResponse,
  type Meta,
  type ObjectItem,
} from '../api'
import { formatMoney, formatPercent, formatPricePerSqm } from '../format'
import { DEV_SIGN } from '../map-legend'
import Dialog from './Dialog'

interface Props {
  meta: Meta
  displayCurrency: string
  highlightId?: number
}

const DEV_ORDER: DevClass[] = ['far_below', 'below', 'near', 'above', 'far_above', 'none']

type SheetPos = 'collapsed' | 'mid' | 'expanded'

function bboxOf(m: maplibregl.Map): string {
  const b = m.getBounds()
  return `${b.getWest()},${b.getSouth()},${b.getEast()},${b.getNorth()}`
}

export default function MapView({ meta, displayCurrency, highlightId }: Props) {
  const { t, i18n } = useTranslation()
  const locale = i18n.language
  const wrapRef = useRef<HTMLDivElement>(null)
  const mapRef = useRef<maplibregl.Map | null>(null)
  const markersRef = useRef<maplibregl.Marker[]>([])
  const [sheet, setSheet] = useState<SheetPos>('collapsed')
  const [data, setData] = useState<MapResponse | null>(null)
  const [moved, setMoved] = useState(false)
  const [q, setQ] = useState('')
  const [country, setCountry] = useState('CZ')
  const [filtersOpen, setFiltersOpen] = useState(false)
  const [selected, setSelected] = useState<ObjectItem | null>(null)
  const [list, setList] = useState<ObjectItem[]>([])
  const [error, setError] = useState<string | null>(null)
  const cfg = meta.map

  const expOf = (c: string | null) => meta.currencies.find((x) => x.code === c)?.exponent ?? 2

  const clearMarkers = () => {
    for (const m of markersRef.current) m.remove()
    markersRef.current = []
  }

  const load = useCallback(
    async (map: maplibregl.Map) => {
      if (!cfg?.style_url) return
      setError(null)
      try {
        const d = await getMapObjects({
          bbox: bboxOf(map),
          zoom: Math.round(map.getZoom()),
          country: country || undefined,
          q: q.trim().length >= 2 ? q.trim() : undefined,
        })
        setData(d)
        setMoved(false)
      } catch (e) {
        setError(String(e))
      }
    },
    [cfg?.style_url, country, q],
  )

  useEffect(() => {
    if (!wrapRef.current || !cfg?.style_url) return
    const map = new maplibregl.Map({
      container: wrapRef.current,
      style: cfg.style_url,
      center: [cfg.default_lng, cfg.default_lat],
      zoom: cfg.default_zoom,
      attributionControl: { compact: true },
    })
    mapRef.current = map
    map.on('load', () => {
      void load(map)
    })
    map.on('moveend', () => setMoved(true))
    return () => {
      clearMarkers()
      map.remove()
      mapRef.current = null
    }
  }, [cfg?.style_url, cfg?.default_lng, cfg?.default_lat, cfg?.default_zoom]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const map = mapRef.current
    if (!map || !data) return
    clearMarkers()

    const addPin = (lng: number, lat: number, cls: DevClass, onClick: () => void, extra?: string) => {
      const el = document.createElement('button')
      el.type = 'button'
      el.className = `map-pin map-pin-${cls}`
      el.textContent = extra ?? DEV_SIGN[cls]
      el.setAttribute('aria-label', cls)
      el.addEventListener('click', (ev) => {
        ev.stopPropagation()
        onClick()
      })
      const mk = new maplibregl.Marker({ element: el }).setLngLat([lng, lat]).addTo(map)
      markersRef.current.push(mk)
    }

    if (data.mode === 'points') {
      for (const p of data.points) {
        addPin(p.lng, p.lat, p.dev_class, () => {
          void getObject(p.id).then((o) => {
            setSelected(o)
            setSheet('mid')
          })
        })
      }
    } else {
      for (const c of data.cells) {
        const cls = (c.mixed ? 'none' : c.dev_class) ?? 'none'
        addPin(c.lng, c.lat, cls, () => setSheet('expanded'), String(c.count))
      }
    }

    const ids = data.areas.map((a) => a.zone_id)
    const src = map.getSource('pb-areas') as maplibregl.GeoJSONSource | undefined
    if (ids.length === 0) {
      if (src) src.setData({ type: 'FeatureCollection', features: [] })
      return
    }
    void getZonesGeoJSON(ids).then((fc) => {
      const byId = new Map<number, MapArea>()
      for (const a of data.areas) byId.set(a.zone_id, a)
      const features = fc.features.map((f) => ({
        ...f,
        properties: {
          ...f.properties,
          count: byId.get(f.properties.id)?.count ?? 0,
          mixed: byId.get(f.properties.id)?.mixed ?? false,
          dev_class: byId.get(f.properties.id)?.dev_class ?? 'none',
        },
      }))
      const gj = { type: 'FeatureCollection' as const, features } as unknown as GeoJSON.FeatureCollection
      if (map.getSource('pb-areas')) {
        ;(map.getSource('pb-areas') as maplibregl.GeoJSONSource).setData(gj)
      } else {
        map.addSource('pb-areas', { type: 'geojson', data: gj })
        const cs = getComputedStyle(document.documentElement)
        const accent = cs.getPropertyValue('--accent').trim() || '#177245'
        const accentHi = cs.getPropertyValue('--accent-hi').trim() || '#b42318'
        const muted = cs.getPropertyValue('--muted').trim() || '#5c6470'
        map.addLayer({
          id: 'pb-areas-fill',
          type: 'fill',
          source: 'pb-areas',
          paint: {
            'fill-color': [
              'match',
              ['get', 'dev_class'],
              'far_below',
              accent,
              'below',
              accent,
              'near',
              muted,
              'above',
              accentHi,
              'far_above',
              accentHi,
              muted,
            ],
            'fill-opacity': 0.28,
          },
        })
        map.addLayer({
          id: 'pb-areas-line',
          type: 'line',
          source: 'pb-areas',
          paint: { 'line-color': '#171a20', 'line-width': 1, 'line-opacity': 0.4 },
        })
        map.on('click', 'pb-areas-fill', (e) => {
          const id = e.features?.[0]?.properties?.id
          if (id) location.hash = `#/zones/${id}`
        })
      }
    })
  }, [data])

  useEffect(() => {
    if (highlightId && mapRef.current) {
      void getObject(highlightId).then((o) => {
        setSelected(o)
        setSheet('mid')
        if (o.lng != null && o.lat != null && o.geom_kind === 'point') {
          mapRef.current?.flyTo({ center: [o.lng, o.lat], zoom: 14 })
        }
      })
    }
  }, [highlightId])

  useEffect(() => {
    if (sheet !== 'expanded' || !mapRef.current) return
    void listObjects({
      country: country || undefined,
      bbox: bboxOf(mapRef.current),
      per_page: 50,
      q: q.trim().length >= 2 ? q.trim() : undefined,
      display_currency: displayCurrency || undefined,
    }).then((p) => setList(p.objects))
  }, [sheet, country, q, displayCurrency, data])

  const searchAddr = async () => {
    if (q.trim().length < 2) return
    const p = await listObjects({ q: q.trim(), country: country || undefined, per_page: 20 })
    setList(p.objects)
    setSheet('expanded')
    const withPt = p.objects.find((o) => o.lat != null && o.lng != null && o.geom_kind === 'point')
    if (withPt && mapRef.current) {
      mapRef.current.flyTo({ center: [withPt.lng!, withPt.lat!], zoom: 13 })
    }
  }

  const myLocation = () => {
    if (!navigator.geolocation || !mapRef.current) return
    navigator.geolocation.getCurrentPosition((pos) => {
      mapRef.current?.flyTo({
        center: [pos.coords.longitude, pos.coords.latitude],
        zoom: 13,
      })
    })
  }

  if (!cfg?.style_url) {
    return <div className="error">{t('map.no_style')}</div>
  }

  const total = data?.total ?? 0

  return (
    <div className="map-page">
      <div className="map-head">
        <label className="map-search">
          <span className="visually-hidden">{t('map.search')}</span>
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void searchAddr()
            }}
            placeholder={t('map.search_ph')}
            aria-label={t('map.search')}
          />
          {q && (
            <button type="button" className="map-clear" onClick={() => setQ('')} aria-label={t('common.clear')}>
              ×
            </button>
          )}
        </label>
      </div>
      <div ref={wrapRef} className="map-canvas" />
      <div className="map-legend" aria-label={t('map.legend')}>
        {DEV_ORDER.map((c) => (
          <span key={c} className={`map-leg-item map-pin-${c}`}>
            <b aria-hidden="true">{DEV_SIGN[c]}</b> {t(`map.class_${c}`)}
          </span>
        ))}
      </div>
      <div className="map-thumb">
        <button type="button" className="map-ctrl" onClick={myLocation}>
          {t('map.my_location')}
        </button>
        {moved && (
          <button type="button" className="map-ctrl" onClick={() => mapRef.current && void load(mapRef.current)}>
            {t('map.search_here')}
          </button>
        )}
        <button type="button" className="map-ctrl" onClick={() => setFiltersOpen(true)}>
          {t('objects.filter_title')}
        </button>
      </div>
      {error && <div className="error map-err">{error}</div>}
      <div className={`map-sheet map-sheet-${sheet}`} role="region" aria-label={t('map.sheet')}>
        <button
          type="button"
          className="sheet-handle"
          onClick={() =>
            setSheet((s) => (s === 'collapsed' ? 'mid' : s === 'mid' ? 'expanded' : 'collapsed'))
          }
        >
          <span className="sheet-grip" aria-hidden="true" />
          <span>
            {t('map.in_view', { count: total })}
          </span>
        </button>
        {sheet === 'mid' && selected && (
          <a className="sheet-card row" href={`#/objects/${selected.id}`}>
            <SheetCard o={selected} locale={locale} expOf={expOf} displayCurrency={displayCurrency} />
          </a>
        )}
        {sheet === 'expanded' && (
          <ul className="object-list sheet-list">
            {list.map((o) => (
              <li key={o.id}>
                <a
                  className="row"
                  href={`#/objects/${o.id}`}
                  onClick={(e) => {
                    if (o.lat != null && o.lng != null && o.geom_kind === 'point' && mapRef.current) {
                      e.preventDefault()
                      mapRef.current.flyTo({ center: [o.lng, o.lat], zoom: 14 })
                      setSelected(o)
                      setSheet('mid')
                    }
                  }}
                >
                  <SheetCard o={o} locale={locale} expOf={expOf} displayCurrency={displayCurrency} />
                </a>
              </li>
            ))}
          </ul>
        )}
      </div>
      <Dialog open={filtersOpen} label={t('objects.filter_title')} onClose={() => setFiltersOpen(false)} className="sheet">
        <div className="field">
          <label>
            {t('objects.filter_country')}
            <select value={country} onChange={(e) => setCountry(e.target.value)}>
              {meta.countries.map((c) => (
                <option key={c} value={c}>
                  {t(`countries.${c}`, { defaultValue: c })}
                </option>
              ))}
            </select>
          </label>
        </div>
        <button
          type="button"
          className="primary"
          onClick={() => {
            setFiltersOpen(false)
            if (mapRef.current) void load(mapRef.current)
          }}
        >
          {t('objects.filter_apply')}
        </button>
      </Dialog>
    </div>
  )
}

function SheetCard({
  o,
  locale,
  expOf,
  displayCurrency,
}: {
  o: ObjectItem
  locale: string
  expOf: (c: string | null) => number
  displayCurrency: string
}) {
  const { t } = useTranslation()
  const price =
    displayCurrency && o.price_display
      ? formatMoney(o.price_display.minor, o.price_display.currency, expOf(o.price_display.currency), locale)
      : o.price_minor != null && o.currency
        ? formatMoney(o.price_minor, o.currency, expOf(o.currency), locale)
        : t('objects.no_price')
  const v = o.valuation
  const dev =
    v?.price_deviation != null ? formatPercent(v.price_deviation, locale) : t('objects.detail_reason_missing')
  const sqm =
    o.price_minor != null && o.currency && o.area_sqm
      ? formatPricePerSqm(o.price_minor, o.currency, expOf(o.currency), locale, o.area_sqm, t('search.sqm'))
      : t('map.no_value')
  return (
    <span className="row-main">
      <span className="row-top">
        <span className={'dev num ' + (v?.price_deviation != null && v.price_deviation < 0 ? 'neg' : v?.price_deviation != null && v.price_deviation > 0 ? 'pos' : '')}>
          {dev}
        </span>
        <span className="price num">{price}</span>
      </span>
      <span className="price-sqm num">{sqm}</span>
      <span className="row-facts">
        {[o.area_sqm != null ? `${o.area_sqm} ${t('search.sqm')}` : null, o.rooms != null ? t('objects.rooms_n', { count: o.rooms }) : null]
          .filter(Boolean)
          .join(' · ')}
      </span>
      <span className="addr">{o.address}</span>
    </span>
  )
}
