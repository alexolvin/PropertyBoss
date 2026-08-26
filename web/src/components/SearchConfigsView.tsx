import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  createSearchConfig,
  deleteSearchConfig,
  listAttributes,
  listSearchConfigs,
  updateSearchConfig,
  type AttributeDef,
  type Meta,
  type SearchConfig,
  type SearchConfigInput,
} from '../api'
import { formatDate } from '../format'
import { fromMinor, toMinor } from '../money'

interface Props {
  meta: Meta
}

// Значения фильтров в форме хранятся строками (из полей ввода);
// в payload они приводятся к типу из реестра атрибутов.
interface FormState {
  id: number | null
  source_id: string | null
  country: string
  deal_type: string
  property_type: string
  min_price: string
  max_price: string
  min_area: string
  max_area: string
  active: boolean
  filters: Record<string, string>
}

function emptyForm(country: string, deal_type: string): FormState {
  return {
    id: null,
    source_id: null,
    country,
    deal_type,
    property_type: '',
    min_price: '',
    max_price: '',
    min_area: '',
    max_area: '',
    active: true,
    filters: {},
  }
}

function toForm(c: SearchConfig, meta: Meta): FormState {
  const code = c.currency ?? meta.market_currencies[c.country]
  const exp = meta.currencies.find((x) => x.code === code)?.exponent ?? 2
  return {
    id: c.id,
    source_id: c.source_id,
    country: c.country,
    deal_type: c.deal_type,
    property_type: c.property_type ?? '',
    min_price: fromMinor(c.min_price_minor, exp),
    max_price: fromMinor(c.max_price_minor, exp),
    min_area: c.min_area_sqm ?? '',
    max_area: c.max_area_sqm ?? '',
    active: c.active,
    filters: filterStrings(c.filter_attributes),
  }
}

function filterStrings(attrs: Record<string, unknown>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(attrs)) {
    if (v === null || v === undefined) continue
    out[k] = v === true ? 'true' : v === false ? '' : String(v)
  }
  return out
}

export default function SearchConfigsView({ meta }: Props) {
  const { t, i18n } = useTranslation()
  const locale = i18n.language
  const [configs, setConfigs] = useState<SearchConfig[] | null>(null)
  const [attrs, setAttrs] = useState<AttributeDef[]>([])
  const [form, setForm] = useState<FormState | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = () => {
    listSearchConfigs().then(setConfigs).catch((e) => setError(String(e)))
  }
  useEffect(load, [])

  const loadAttrs = (country: string) => {
    listAttributes(country)
      .then(setAttrs)
      .catch((e) => setError(String(e)))
  }
  useEffect(() => {
    if (form) loadAttrs(form.country)
  }, [form?.country]) // eslint-disable-line react-hooks/exhaustive-deps

  const marketCurrency = form ? (meta.market_currencies[form.country] ?? '') : ''
  const exponent =
    meta.currencies.find((c) => c.code === marketCurrency)?.exponent ?? 2

  const set = (patch: Partial<FormState>) =>
    setForm((f) => (f ? { ...f, ...patch } : f))

  const submit = async () => {
    if (!form) return
    setError(null)

    const input: SearchConfigInput = {
      source_id: form.source_id,
      country: form.country,
      deal_type: form.deal_type,
      property_type: form.property_type.trim() || null,
      filter_attributes: {},
      min_area_sqm: null,
      max_area_sqm: null,
      min_price_minor: null,
      max_price_minor: null,
      currency: null,
      active: form.active,
    }

    // Цена: либо обе границы, либо ни одной (целые минорные единицы, ТЗ §5)
    const hasMin = form.min_price.trim() !== ''
    const hasMax = form.max_price.trim() !== ''
    if (hasMin !== hasMax) {
      setError(t('search.price_pair'))
      return
    }
    if (hasMin || hasMax) {
      const mn = toMinor(form.min_price, exponent)
      const mx = toMinor(form.max_price, exponent)
      if (mn === null || mx === null || mn > mx) {
        setError(t('search.price_invalid'))
        return
      }
      input.min_price_minor = mn
      input.max_price_minor = mx
      input.currency = marketCurrency
    }

    // Площадь: либо обе, либо ни одной (строка — NUMERIC точно)
    const aMin = form.min_area.trim()
    const aMax = form.max_area.trim()
    if (aMin !== '' || aMax !== '') {
      if (aMin === '' || aMax === '') {
        setError(t('search.area_pair'))
        return
      }
      input.min_area_sqm = aMin
      input.max_area_sqm = aMax
    }

    // Фильтры — только ключи из реестра, тип по реестру (ТЗ §6)
    const filterPayload: Record<string, unknown> = {}
    for (const def of attrs) {
      const raw = form.filters[def.key]
      if (raw === undefined || raw === '') continue
      switch (def.data_type) {
        case 'bool':
          if (raw === 'true') filterPayload[def.key] = true
          break
        case 'int': {
          const n = Number(raw)
          if (!Number.isInteger(n)) {
            setError(t('search.attr_int', { key: def.key }))
            return
          }
          filterPayload[def.key] = n
          break
        }
        case 'float': {
          const n = Number(raw)
          if (Number.isNaN(n)) {
            setError(t('search.attr_float', { key: def.key }))
            return
          }
          filterPayload[def.key] = n
          break
        }
        case 'enum':
          filterPayload[def.key] = raw
          break
      }
    }
    input.filter_attributes = filterPayload

    try {
      if (form.id === null) {
        await createSearchConfig(input)
      } else {
        await updateSearchConfig(form.id, input)
      }
      setForm(null)
      load()
    } catch (e) {
      setError(String(e))
    }
  }

  const remove = async (id: number) => {
    try {
      await deleteSearchConfig(id)
      load()
    } catch (e) {
      setError(String(e))
    }
  }

  const labelOf = (d: AttributeDef) => (locale.startsWith('ru') ? d.label_ru : d.label_en)

  return (
    <>
      <div className="card">
        <div className="toolbar">
          <button
            className="primary"
            onClick={() =>
              setForm(emptyForm(meta.countries[0] ?? '', meta.deal_types[0] ?? ''))
            }
          >
            {t('search.new')}
          </button>
          {configs && configs.length === 0 && (
            <span className="muted">{t('search.no_configs')}</span>
          )}
        </div>
        {error && <div className="error">{error}</div>}
        {configs && (
          <table>
            <thead>
              <tr>
                <th>#</th>
                <th>{t('search.country')}</th>
                <th>{t('search.deal_type')}</th>
                <th>{t('search.property_type')}</th>
                <th>{t('search.price_min')}</th>
                <th>{t('search.source')}</th>
                <th>{t('search.status')}</th>
                <th>{t('search.updated')}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {configs.map((c) => {
                const exp = meta.currencies.find(
                  (x) => x.code === (c.currency ?? meta.market_currencies[c.country]),
                )?.exponent ?? 2
                return (
                  <tr key={c.id}>
                    <td>{c.id}</td>
                    <td>{c.country}</td>
                    <td>{t(`search.deal_${c.deal_type}`, { defaultValue: c.deal_type })}</td>
                    <td>{c.property_type ?? '—'}</td>
                    <td>
                      {c.min_price_minor !== null ? (
                        `${fromMinor(c.min_price_minor, exp)} – ${fromMinor(c.max_price_minor, exp)} ${c.currency}`
                      ) : (
                        '—'
                      )}
                    </td>
                    <td>
                      {c.source_id ?? (
                        <span className="muted">{t('search.source_none')}</span>
                      )}
                    </td>
                    <td>
                      {c.active ? (
                        <span className="tag">{t('search.on')}</span>
                      ) : (
                        <span className="tag off">{t('search.off')}</span>
                      )}
                    </td>
                    <td>{formatDate(c.updated_at, locale)}</td>
                    <td>
                      <button className="ghost" onClick={() => setForm(toForm(c, meta))}>
                        {t('search.edit')}
                      </button>{' '}
                      <button className="ghost danger" onClick={() => remove(c.id)}>
                        {t('search.deactivate')}
                      </button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </div>

      {form && (
        <div className="card">
          <h3>{form.id === null ? t('search.new') : t('search.edit')}</h3>
          <div className="form-grid">
            <label>
              {t('search.country')}
              <select
                value={form.country}
                onChange={(e) =>
                  set({ country: e.target.value, filters: {} })
                }
              >
                {meta.countries.map((c) => (
                  <option key={c} value={c}>
                    {c}
                  </option>
                ))}
              </select>
            </label>
            <label>
              {t('search.deal_type')}
              <select
                value={form.deal_type}
                onChange={(e) => set({ deal_type: e.target.value })}
              >
                {meta.deal_types.map((d) => (
                  <option key={d} value={d}>
                    {t(`search.deal_${d}`, { defaultValue: d })}
                  </option>
                ))}
              </select>
            </label>
            <label>
              {t('search.property_type')}
              <input
                value={form.property_type}
                placeholder="house / apartment / …"
                onChange={(e) => set({ property_type: e.target.value })}
              />
            </label>
            <label>
              {t('search.price_min', { currency: marketCurrency })}
              <input
                inputMode="decimal"
                value={form.min_price}
                onChange={(e) => set({ min_price: e.target.value })}
              />
            </label>
            <label>
              {t('search.price_max', { currency: marketCurrency })}
              <input
                inputMode="decimal"
                value={form.max_price}
                onChange={(e) => set({ max_price: e.target.value })}
              />
            </label>
            <label>
              {t('search.area_min')}
              <input
                inputMode="decimal"
                value={form.min_area}
                onChange={(e) => set({ min_area: e.target.value })}
              />
            </label>
            <label>
              {t('search.area_max')}
              <input
                inputMode="decimal"
                value={form.max_area}
                onChange={(e) => set({ max_area: e.target.value })}
              />
            </label>
            <label>
              {t('search.active')}
              <input
                type="checkbox"
                checked={form.active}
                onChange={(e) => set({ active: e.target.checked })}
              />
            </label>
          </div>

          <h4>{t('search.filters')}</h4>
          {attrs.length === 0 ? (
            <p className="muted">{t('search.no_filters')}</p>
          ) : (
            <div className="form-grid">
              {attrs.map((d) => (
                <label key={d.key} title={d.source_evidence}>
                  {labelOf(d)}
                  {d.data_type === 'bool' ? (
                    <input
                      type="checkbox"
                      checked={form.filters[d.key] === 'true'}
                      onChange={(e) =>
                        set({
                          filters: {
                            ...form.filters,
                            [d.key]: e.target.checked ? 'true' : '',
                          },
                        })
                      }
                    />
                  ) : d.data_type === 'enum' ? (
                    <select
                      value={form.filters[d.key] ?? ''}
                      onChange={(e) =>
                        set({ filters: { ...form.filters, [d.key]: e.target.value } })
                      }
                    >
                      <option value="">—</option>
                      {(d.allowed_values ?? []).map((v) => (
                        <option key={v} value={v}>
                          {v}
                        </option>
                      ))}
                    </select>
                  ) : (
                    <input
                      type="number"
                      step={d.data_type === 'int' ? 1 : 'any'}
                      value={form.filters[d.key] ?? ''}
                      onChange={(e) =>
                        set({ filters: { ...form.filters, [d.key]: e.target.value } })
                      }
                    />
                  )}
                </label>
              ))}
            </div>
          )}

          <div className="toolbar" style={{ marginTop: 12 }}>
            <button className="primary" onClick={submit}>
              {t('search.save')}
            </button>
            <button className="ghost" onClick={() => setForm(null)}>
              {t('search.cancel')}
            </button>
          </div>
        </div>
      )}
    </>
  )
}
