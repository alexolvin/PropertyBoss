import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import maplibregl from 'maplibre-gl'
import 'maplibre-gl/dist/maplibre-gl.css'
import type { Meta, ObjectItem } from '../api'
import { DEV_SIGN } from '../map-legend'

interface Props {
  o: ObjectItem
  meta: Meta
}

export default function MiniMap({ o, meta }: Props) {
  const { t } = useTranslation()
  const ref = useRef<HTMLDivElement>(null)
  const style = meta.map?.style_url
  const isPoint = o.geom_kind === 'point' && o.lat != null && o.lng != null

  useEffect(() => {
    if (!ref.current || !style) return
    const center: [number, number] = isPoint
      ? [o.lng!, o.lat!]
      : [meta.map!.default_lng, meta.map!.default_lat]
    const map = new maplibregl.Map({
      container: ref.current,
      style,
      center,
      zoom: isPoint ? 14 : 9,
      interactive: false,
      attributionControl: false,
    })
    if (isPoint) {
      const el = document.createElement('span')
      el.className = 'map-pin map-pin-none'
      el.textContent = DEV_SIGN.none
      new maplibregl.Marker({ element: el }).setLngLat(center).addTo(map)
    }
    return () => map.remove()
  }, [style, isPoint, o.lat, o.lng, meta.map])

  if (!style) return null
  return (
    <div className="mini-map-wrap">
      <div ref={ref} className="mini-map" />
      <p className="dev-meta">
        {t('map.geo_level')}: {o.geo_confidence ? t(`map.conf_${o.geo_confidence}`) : t('map.conf_none')}
        {o.geom_kind === 'area' && ` — ${t('map.area_not_point')}`}
      </p>
    </div>
  )
}
