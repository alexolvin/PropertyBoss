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
  filter_attributes: Record<string, unknown>
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

export interface ObjectItem {
  id: number
  country: string
  deal_type: string
  zone_id: number | null
  zone_name: string | null
  zone_level: string | null
  zone_source: string | null
  address: string | null
  area_sqm: number | null
  rooms: number | null
  property_type: string | null
  price_minor: number | null
  currency: string | null
  price_display: PriceDisplay | null
  valuation: Valuation | null
  hazard: Hazard | null
  status: 'active' | 'delisted'
  delisted_reason: string | null
  first_seen_at: string
  last_seen_at: string
  delisted_at: string | null
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
  page?: number
  per_page?: number
}

export function listZones(params: ZonesParams): Promise<ZonesPage> {
  const qs = new URLSearchParams()
  if (params.country) qs.set('country', params.country)
  if (params.level) qs.set('level', params.level)
  qs.set('page', String(params.page ?? 1))
  qs.set('per_page', String(params.per_page ?? 50))
  return get<ZonesPage>(`/api/zones?${qs.toString()}`)
}

const HEADERS = { 'Content-Type': 'application/json' } as const

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path)
  if (!res.ok) throw new Error(await errText(res))
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
}

export function listObjects(params: ObjectsParams): Promise<ObjectsPage> {
  const qs = new URLSearchParams()
  if (params.country) qs.set('country', params.country)
  if (params.status) qs.set('status', params.status)
  qs.set('page', String(params.page ?? 1))
  qs.set('per_page', String(params.per_page ?? 50))
  if (params.display_currency) qs.set('display_currency', params.display_currency)
  return get<ObjectsPage>(`/api/objects?${qs.toString()}`)
}
