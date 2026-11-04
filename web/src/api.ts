// API-клиент. Деньги — целые минорные единицы + валюта (ТЗ §5):
// в JS суммы передаются целым числом, не дробным number.

export interface CurrencyInfo {
  code: string
  exponent: number
}

export interface Meta {
  currencies: CurrencyInfo[]
  countries: string[]
  market_currencies: Record<string, string>
  deal_types: string[]
  fx_last_rate_date: string | null
  // Версия сборки, которую сервер раздаёт (TZ UI v5 U02): сверка
  // «что я смотрю» — бандл против .build-info.json на сервере.
  ui_build?: { commit: string; built_at: string } | null
  map?: {
    style_url: string
    points_max: number
    default_lng: number
    default_lat: number
    default_zoom: number
    zone_color_min_n: number
  }
}

export interface AttributeDef {
  key: string
  data_type: 'bool' | 'enum' | 'int' | 'float'
  allowed_values?: string[]
  used_in_pricing: boolean
  label_ru: string
  label_en: string
  source_evidence: string
}

export interface SearchConfig {
  id: number
  source_id: string | null
  country: string
  deal_type: string
  property_type: string | null
  // API отдаёт null, когда фильтры не заданы (Go nil map) — не {}.
  filter_attributes: Record<string, unknown> | null
  min_area_sqm: string | null
  max_area_sqm: string | null
  min_price_minor: number | null
  max_price_minor: number | null
  currency: string | null
  active: boolean
  created_at: string
  updated_at: string
}

export type SearchConfigInput = Omit<SearchConfig, 'id' | 'created_at' | 'updated_at'>

export interface PriceDisplay {
  minor: number
  currency: string
  derived: boolean
  rate_date: string
  rate_stale: boolean
}

/**
 * Последняя оценка объекта (valuations, ТЗ §7.3):
 * price_deviation хранится ВМЕСТЕ с интервалом, sample_size и r_squared —
 * дашборд не показывает число без интервала.
 */
export interface Valuation {
  model_version: string | null
  price_deviation: number | null
  null_reason: string | null
  predicted_price_minor: number | null
  interval_low_minor: number | null
  interval_high_minor: number | null
  sample_size: number
  r_squared: number | null
  zone_fallback: boolean
  computed_at: string | null
}

/**
 * Вероятность ухода объекта с рынка за horizon_days дней
 * (liquidity_estimates, ТЗ §9.2–9.3): NULL с причиной, пока модель
 * не опубликована. Предсказанное — уход с рынка, НЕ продажа.
 */
export interface Hazard {
  horizon_days: number | null
  probability: number | null
  null_reason?: string | null
  model_version: string | null
  events_in_training: number
  computed_at: string | null
}

/**
 * Фото объекта (object_photos, ТЗ v4 §4.4): ссылка на источник всегда есть,
 * локальные копии отдаёт pb serve под /photos (два размера: list — строка
 * списка, card — фид на экране объекта).
 */
export interface Photo {
  ord: number
  original_url: string
  card_url?: string | null
  list_url?: string | null
}

/** Точка истории цены (price_history, TZ_UI v3 §6.3.2). */
export interface PricePoint {
  price_minor: number
  currency: string
  change_at: string
}

/** Объявление объекта на площадке (object_listings, TZ_UI v3 §6.3.6). */
export interface Listing {
  source_id: string
  source_name: string
  external_id: string
  source_url?: string | null
}

export interface ObjectItem {
  id: number
  country: string
  deal_type: string
  zone_id: number | null
  zone_name: string | null
  zone_level: string | null
  zone_source: string | null
  title?: string | null
  address: string | null
  attributes?: Record<string, unknown> | null
  attributes_unmapped?: Record<string, unknown> | null
  area_sqm: number | null
  rooms: number | null
  property_type: string | null
  // Тип продавца (private/agency/developer) и название агентства —
  // колонки objects, заполняются экстракцией (ТЗ v4 §4.7).
  seller_type?: string | null
  seller_name?: string | null
  price_minor: number | null
  currency: string | null
  price_display: PriceDisplay | null
  valuation: Valuation | null
  // C25: объект без уровня геокода — структурно не моделируем;
  // valuation принудительно null, флаг показывается на экране.
  not_modeled: boolean
  hazard: Hazard | null
  status: 'active' | 'delisted'
  delisted_reason: string | null
  first_seen_at: string
  last_seen_at: string
  delisted_at: string | null
  // TZ_UI v3 §6.2: признак изменения цены в списке («цена снижена»).
  price_trend?: 'reduced' | 'increased'
  // TZ_UI v3 §6.3.2/§6.3.6: заполнены только для GET /api/objects/{id}.
  price_history?: PricePoint[]
  listings?: Listing[]
  // ТЗ v4 §4.4: обложка (первое фото) в списке; фид — только в детальной.
  description_original?: string | null
  cover_photo?: Photo | null
  photos?: Photo[]
  lat?: number | null
  lng?: number | null
  geo_confidence?: string | null
  geom_kind?: 'point' | 'area' | 'none'
}

export interface ObjectsPage {
  total: number
  page: number
  per_page: number
  objects: ObjectItem[]
}

/** Точка калибровочной кривой: дециль предсказанной вероятности (ТЗ §9.4). */
export interface LiquidityCalibDecile {
  decile: number
  predicted: number
  actual: number
  n: number
}

export interface LiquidityBrierDecomp {
  reliability: number
  resolution: number
  uncertainty: number
}

/**
 * Последняя версия модели ликвидности (liquidity_models, ТЗ §9.4):
 * метрики валидации хранятся вместе с версией модели.
 */
export interface LiquidityModel {
  model_version: string
  country: string
  deal_type: string
  status: 'published' | 'uncalibrated' | 'insufficient_history'
  reject_reason?: string | null
  horizon_days: number
  min_events: number
  n_completed_events: number
  n_person_periods: number
  n_params: number | null
  train_cutoff_at: string | null
  n_train: number
  n_test: number
  calibration: LiquidityCalibDecile[] | null
  max_calib_dev: number | null
  brier_score: number | null
  brier_decomp: LiquidityBrierDecomp | null
  c_index: number | null
  params: Record<string, number> | null
  computed_at: string
}

export interface LiquidityParams {
  country?: string
  deal_type?: string
}

export function getLiquidity(params: LiquidityParams): Promise<{ model: LiquidityModel | null }> {
  const qs = new URLSearchParams()
  if (params.country) qs.set('country', params.country)
  if (params.deal_type) qs.set('deal_type', params.deal_type)
  const s = qs.toString()
  return get<{ model: LiquidityModel | null }>(`/api/liquidity${s ? `?${s}` : ''}`)
}

export interface ZoneItem {
  id: number
  country: string
  level: 'region' | 'municipality' | 'zone'
  name: string
  external_code: string | null
  parent_name: string | null
  source: string
}

export interface ZonesPage {
  total: number
  page: number
  per_page: number
  zones: ZoneItem[]
  /** Источники данных зон (атрибуция в UI, ТЗ §13). */
  sources: string[]
}

export interface ZonesParams {
  country?: string
  level?: string
  /** Поиск по названию/коду (TZ_UI v3 §6.6): выдача до 20, без страниц. */
  q?: string
  page?: number
  per_page?: number
}

export function listZones(params: ZonesParams): Promise<ZonesPage> {
  const qs = new URLSearchParams()
  if (params.country) qs.set('country', params.country)
  if (params.level) qs.set('level', params.level)
  if (params.q) qs.set('q', params.q)
  qs.set('page', String(params.page ?? 1))
  qs.set('per_page', String(params.per_page ?? (params.q ? 20 : 50)))
  return get<ZonesPage>(`/api/zones?${qs.toString()}`)
}

const HEADERS = { 'Content-Type': 'application/json' } as const

// TZ UI v5 U09: классификация ошибок — сервер (причина в теле ответа),
// network (fetch отклонён) и offline (fetch отклонён при
// navigator.onLine === false) — сообщение об офлайне отличается от
// ошибки сервера.
export class ApiError extends Error {
  kind: 'server' | 'network' | 'offline'
  constructor(kind: 'server' | 'network' | 'offline', message: string) {
    super(message)
    this.kind = kind
  }
}

// TZ UI v5 U10: «данные на HH:MM» — из заголовка Date ответа сервера,
// не из часов клиента. Обновляется только при успешном ответе.
let serverTime: Date | null = null
export function lastServerTime(): Date | null {
  return serverTime
}

async function get<T>(path: string): Promise<T> {
  let res: Response
  try {
    res = await fetch(path)
  } catch {
    throw new ApiError(navigator.onLine === false ? 'offline' : 'network', 'network error')
  }
  if (!res.ok) throw new ApiError('server', await errText(res))
  const d = res.headers.get('Date')
  if (d) {
    const dt = new Date(d)
    if (!Number.isNaN(dt.getTime())) serverTime = dt
  }
  return (await res.json()) as T
}

async function errText(res: Response): Promise<string> {
  try {
    const body = (await res.json()) as { error?: string }
    return body.error ?? `${res.status} ${res.statusText}`
  } catch {
    return `${res.status} ${res.statusText}`
  }
}

export function getMeta(): Promise<Meta> {
  return get<Meta>('/api/meta')
}

export function listAttributes(country: string): Promise<AttributeDef[]> {
  return get<AttributeDef[]>(`/api/attribute-registry?country=${encodeURIComponent(country)}`)
}

export function listSearchConfigs(): Promise<SearchConfig[]> {
  return get<SearchConfig[]>('/api/search-configs')
}

export async function createSearchConfig(input: SearchConfigInput): Promise<number> {
  const res = await fetch('/api/search-configs', {
    method: 'POST',
    headers: HEADERS,
    body: JSON.stringify(input),
  })
  if (!res.ok) throw new Error(await errText(res))
  const body = (await res.json()) as { id: number }
  return body.id
}

export async function updateSearchConfig(id: number, input: SearchConfigInput): Promise<void> {
  const res = await fetch(`/api/search-configs/${id}`, {
    method: 'PUT',
    headers: HEADERS,
    body: JSON.stringify(input),
  })
  if (!res.ok && res.status !== 204) throw new Error(await errText(res))
}

export async function deleteSearchConfig(id: number): Promise<void> {
  const res = await fetch(`/api/search-configs/${id}`, { method: 'DELETE' })
  if (!res.ok && res.status !== 204) throw new Error(await errText(res))
}

export interface ObjectsParams {
  country?: string
  status?: string
  page?: number
  per_page?: number
  display_currency?: string
  q?: string
  sort?: string
  bbox?: string
}

export function listObjects(params: ObjectsParams): Promise<ObjectsPage> {
  const qs = new URLSearchParams()
  if (params.country) qs.set('country', params.country)
  if (params.status) qs.set('status', params.status)
  qs.set('page', String(params.page ?? 1))
  qs.set('per_page', String(params.per_page ?? 50))
  if (params.display_currency) qs.set('display_currency', params.display_currency)
  if (params.q) qs.set('q', params.q)
  if (params.sort) qs.set('sort', params.sort)
  if (params.bbox) qs.set('bbox', params.bbox)
  return get<ObjectsPage>(`/api/objects?${qs.toString()}`)
}

export type DevClass = 'far_below' | 'below' | 'near' | 'above' | 'far_above' | 'none'

export interface MapPoint {
  id: number
  lat: number
  lng: number
  dev_class: DevClass
  has_valuation: boolean
}

export interface MapCell {
  lat: number
  lng: number
  count: number
  dev_class: DevClass | null
  mixed: boolean
}

export interface MapArea {
  zone_id: number
  count: number
  dev_class: DevClass | null
  mixed: boolean
}

export interface MapResponse {
  mode: 'points' | 'cells'
  total: number
  points: MapPoint[]
  cells: MapCell[]
  areas: MapArea[]
}

export interface MapQuery {
  bbox: string
  zoom: number
  country?: string
  status?: string
  q?: string
}

export function getMapObjects(p: MapQuery): Promise<MapResponse> {
  const qs = new URLSearchParams()
  qs.set('bbox', p.bbox)
  qs.set('zoom', String(p.zoom))
  if (p.country) qs.set('country', p.country)
  if (p.status) qs.set('status', p.status)
  if (p.q) qs.set('q', p.q)
  return get<MapResponse>(`/api/objects/map?${qs.toString()}`)
}

export interface ZoneCard {
  zone: ZoneItem
  n_objects: number
  median_price_minor: number | null
  avg_price_minor: number | null
  avg_ppsm_minor: number | null
  currency: string | null
  trend_down_share: number | null
  trend_reason: string | null
  trend_n: number
  zone_color_min_n: number
}

export function getZone(id: number): Promise<ZoneCard> {
  return get<ZoneCard>(`/api/zones/${id}`)
}

export interface ZoneFeatureCollection {
  type: 'FeatureCollection'
  features: {
    type: 'Feature'
    id: number
    properties: { id: number; name: string }
    geometry: object
  }[]
}

export function getZonesGeoJSON(ids: number[]): Promise<ZoneFeatureCollection> {
  return get<ZoneFeatureCollection>(`/api/zones/geojson?ids=${ids.join(',')}`)
}

/** Экран объекта (TZ_UI v3 §6.3): история цены и объявления на площадках. */
export function getObject(id: number): Promise<ObjectItem> {
  return get<ObjectItem>(`/api/objects/${id}`)
}

export interface SourceItem {
  id: string
  name: string
  domain: string
  country: string
  deal_types: string[]
  kind: string
  state: string
  last_complete_scan_at?: string | null
}

export function listSources(): Promise<SourceItem[]> {
  return get<SourceItem[]>('/api/sources')
}

/** Состояние моделей по парам «страна — тип сделки» (TZ_UI v3 §6.5). */
export interface PairModels {
  country: string
  deal_type: string
  object_count: number
  deviation?: {
    n_valued: number
    n_with_deviation: number
    dominant_reason?: string
    model_version?: string
    r_squared?: number | null
    sample_size?: number
    n_zone_fallback: number
  } | null
  liquidity?: {
    model_version: string
    status: 'published' | 'uncalibrated' | 'insufficient_history'
    reject_reason?: string | null
    horizon_days: number
    min_events: number
    n_completed_events: number
    brier_score?: number | null
    c_index?: number | null
    max_calib_dev?: number | null
    calibration?: LiquidityCalibDecile[]
    computed_at: string
  } | null
}

export interface ModelsInfo {
  pairs: PairModels[]
  sources: SourceItem[]
}

export function listModels(): Promise<ModelsInfo> {
  return get<ModelsInfo>('/api/models')
}
