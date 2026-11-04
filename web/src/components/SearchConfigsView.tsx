import { Fragment, useEffect, useState } from 'react'
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
import Dialog from './Dialog'

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

/** Строка «было → стало» в подтверждении правки (TZ_UI v5 U22). */
interface DiffRow {
  label: string
  was: string
  now: string
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

function filterStrings(attrs: Record<string, unknown> | null): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(attrs ?? {})) {
    if (v === null || v === undefined) continue
    out[k] = v === true ? 'true' : v === false ? '' : String(v)
  }
  return out
}

export default function SearchConfigsView({ meta }: Props) {
  const { t, i18n } = useTranslation()
  const locale = i18n.language
  const [configs, setConfigs] = useState<SearchConfig[] | null>(null)
  // Атрибуты страны формы (для полей фильтров в модалке).
  const [attrs, setAttrs] = useState<AttributeDef[]>([])
  // Атрибуты стран карточек (U23: подписи фильтров — из реестра,
  // а не машинные ключи).
  const [attrsByCountry, setAttrsByCountry] = useState<Record<string, AttributeDef[]>>({})
  // TZ_UI v5 U22: правка — ТОЛЬКО в модалке (примитив U07), с подтверждением
  // «было → стало»; inline-форма в конце списка убрана (D6).
  const [modal, setModal] = useState<{
    form: FormState
    baseline: SearchConfig | null
    initial: FormState
  } | null>(null)
  const [modalMode, setModalMode] = useState<'form' | 'diff' | 'confirm-close'>('form')
  const [diffRows, setDiffRows] = useState<DiffRow[]>([])
  const [error, setError] = useState<string | null>(null)
  // Обратная связь после сохранения: сообщение + подсветка записи.
  const [savedMsg, setSavedMsg] = useState<string | null>(null)
  const [highlightId, setHighlightId] = useState<number | null>(null)

  const load = () => {
    listSearchConfigs().then(setConfigs).catch((e) => setError(String(e)))
  }
  useEffect(load, [])

  // Реестры стран, представленных в карточках (один запрос на страну).
  useEffect(() => {
    if (!configs) return
    const missing = [...new Set(configs.map((c) => c.country))].filter((c) => !(c in attrsByCountry))
    if (missing.length === 0) return
    let cancelled = false
    Promise.all(
      missing.map(async (c) => [c, await listAttributes(c).catch(() => [] as AttributeDef[])] as const),
    ).then((entries) => {
      if (!cancelled) setAttrsByCountry((m) => ({ ...m, ...Object.fromEntries(entries) }))
    })
    return () => {
      cancelled = true
    }
  }, [configs, attrsByCountry])

  // Реестр страны, открытой в форме (поля фильтров).
  const formCountry = modal?.form.country
  const loadAttrs = (country: string) => {
    listAttributes(country)
      .then(setAttrs)
      .catch((e) => setError(String(e)))
  }
  useEffect(() => {
    if (formCountry) loadAttrs(formCountry)
  }, [formCountry]) // eslint-disable-line react-hooks/exhaustive-deps

  const marketCurrencyOf = (country: string) => meta.market_currencies[country] ?? ''
  const marketCurrency = formCountry ? marketCurrencyOf(formCountry) : ''

  const set = (patch: Partial<FormState>) =>
    setModal((m) => (m ? { ...m, form: { ...m.form, ...patch } } : m))

  const labelOf = (d: AttributeDef) => (locale.startsWith('ru') ? d.label_ru : d.label_en)
  const labelOfByCountry = (country: string, key: string): string => {
    const def = attrsByCountry[country]?.find((d) => d.key === key)
    return def ? labelOf(def) : key
  }

  // Плейлоад из формы: валидация (цены/площадь парами, типы — по реестру).
  const buildInput = (f: FormState): SearchConfigInput | { error: string } => {
    const input: SearchConfigInput = {
      source_id: f.source_id,
      country: f.country,
      deal_type: f.deal_type,
      property_type: f.property_type.trim() || null,
      filter_attributes: {},
      min_area_sqm: null,
      max_area_sqm: null,
      min_price_minor: null,
      max_price_minor: null,
      currency: null,
      active: f.active,
    }
    const exp =
      meta.currencies.find((x) => x.code === marketCurrencyOf(f.country))?.exponent ?? 2

    // Цена: либо обе границы, либо ни одной (целые минорные единицы, ТЗ §5)
    const hasMin = f.min_price.trim() !== ''
    const hasMax = f.max_price.trim() !== ''
    if (hasMin !== hasMax) return { error: t('search.price_pair') }
    if (hasMin || hasMax) {
      const mn = toMinor(f.min_price, exp)
      const mx = toMinor(f.max_price, exp)
      if (mn === null || mx === null || mn > mx) return { error: t('search.price_invalid') }
      input.min_price_minor = mn
      input.max_price_minor = mx
      input.currency = marketCurrencyOf(f.country)
    }

    // Площадь: либо обе, либо ни одной (строка — NUMERIC точно)
    const aMin = f.min_area.trim()
    const aMax = f.max_area.trim()
    if (aMin !== '' || aMax !== '') {
      if (aMin === '' || aMax === '') return { error: t('search.area_pair') }
      input.min_area_sqm = aMin
      input.max_area_sqm = aMax
    }

    // Фильтры — только ключи из реестра, тип по реестру (ТЗ §6)
    const filterPayload: Record<string, unknown> = {}
    for (const def of attrs) {
      const raw = f.filters[def.key]
      if (raw === undefined || raw === '') continue
      switch (def.data_type) {
        case 'bool':
          if (raw === 'true') filterPayload[def.key] = true
          break
        case 'int': {
          const n = Number(raw)
          if (!Number.isInteger(n)) return { error: t('search.attr_int', { key: def.key }) }
          filterPayload[def.key] = n
          break
        }
        case 'float': {
          const n = Number(raw)
          if (Number.isNaN(n)) return { error: t('search.attr_float', { key: def.key }) }
          filterPayload[def.key] = n
          break
        }
        case 'enum':
          filterPayload[def.key] = raw
          break
      }
    }
    input.filter_attributes = filterPayload
    return input
  }

  // «Было → стало» по каждому изменённому полю (TЗ_UI v5 U22, ТЗ v4 §7.1):
  // сравнение формы с исходной формой той же записи, а не с сырыми
  // значениями API — строки уже в формате формы.
  const formDiff = (a: FormState, b: FormState): DiffRow[] => {
    const rows: DiffRow[] = []
    const add = (label: string, was: string, now: string) => {
      if (was !== now) {
        rows.push({
          label,
          was: was === '' ? t('search.empty_value') : was,
          now: now === '' ? t('search.empty_value') : now,
        })
      }
    }
    const onOff = (x: boolean) => (x ? t('search.on') : t('search.off'))
    add(t('search.country'), a.country, b.country)
    add(
      t('search.deal_type'),
      t(`search.deal_${a.deal_type}`, { defaultValue: a.deal_type }),
      t(`search.deal_${b.deal_type}`, { defaultValue: b.deal_type }),
    )
    add(t('search.property_type'), a.property_type.trim(), b.property_type.trim())
    add(t('search.price_min', { currency: marketCurrencyOf(a.country) }), a.min_price, b.min_price)
    add(t('search.price_max', { currency: marketCurrencyOf(a.country) }), a.max_price, b.max_price)
    add(t('search.area_min'), a.min_area, b.min_area)
    add(t('search.area_max'), a.max_area, b.max_area)
    add(t('search.active'), onOff(a.active), onOff(b.active))
    const keys = new Set([...Object.keys(a.filters), ...Object.keys(b.filters)])
    for (const k of [...keys].sort()) {
      const def = attrs.find((d) => d.key === k)
      const val = (v: string | undefined) => {
        const x = v ?? ''
        if (def?.data_type === 'bool') return x === 'true' ? t('common.yes') : t('search.empty_value')
        return x
      }
      add(def ? labelOf(def) : k, val(a.filters[k]), val(b.filters[k]))
    }
    return rows
  }

  const isDirty = () => (modal ? formDiff(modal.initial, modal.form).length > 0 : false)

  const closeModal = () => {
    setModal(null)
    setModalMode('form')
    setDiffRows([])
  }

  // Закрытие с несохранёнными изменениями — подтверждение (ТЗ v4 §7.1):
  // «изменения не сохранены», остаться/закрыть.
  const requestClose = () => {
    if (!modal) return
    if (isDirty()) setModalMode('confirm-close')
    else closeModal()
  }

  const configName = (f: FormState) =>
    `${t(`countries.${f.country}`, { defaultValue: f.country })} — ${t(
      `search.deal_${f.deal_type}`,
      { defaultValue: f.deal_type },
    )}`

  const apply = async (input: SearchConfigInput, form: FormState) => {
    let id: number
    try {
      if (form.id === null) {
        id = await createSearchConfig(input)
        setSavedMsg(t('search.created_msg', { name: configName(form) }))
      } else {
        id = form.id
        await updateSearchConfig(id, input)
        setSavedMsg(t('search.saved', { name: configName(form) }))
      }
    } catch (e) {
      setError(String(e))
      return
    }
    closeModal()
    load()
    // Подсветка сохранённой записи на короткое время (ТЗ v4 §7.1).
    setHighlightId(id)
    window.setTimeout(() => setHighlightId(null), 2000)
  }

  const onSave = async () => {
    if (!modal) return
    setError(null)
    const built = buildInput(modal.form)
    if ('error' in built) {
      setError(built.error)
      return
    }
    // Правка: пользователь подтверждает именно список «было → стало».
    if (modal.baseline) {
      const rows = formDiff(toForm(modal.baseline, meta), modal.form)
      if (rows.length > 0) {
        setDiffRows(rows)
        setModalMode('diff')
        return
      }
    }
    await apply(built, modal.form)
  }

  const openNew = () => {
    setSavedMsg(null)
    setError(null)
    const f = emptyForm(meta.countries[0] ?? '', meta.deal_types[0] ?? '')
    setModal({ form: f, baseline: null, initial: f })
    setModalMode('form')
  }
  const openEdit = (c: SearchConfig) => {
    setSavedMsg(null)
    setError(null)
    const f = toForm(c, meta)
    setModal({ form: f, baseline: c, initial: f })
    setModalMode('form')
  }

  // DELETE — мягкое выключение (active = false, история сканов не рвётся);
  // включение — PUT полным payload'ом из карточки. Подпись кнопки — по
  // состоянию, а не константа «Отключить» (критика Этапа 5).
  const toggle = async (c: SearchConfig) => {
    try {
      if (c.active) {
        await deleteSearchConfig(c.id)
      } else {
        await updateSearchConfig(c.id, {
          source_id: c.source_id,
          country: c.country,
          deal_type: c.deal_type,
          property_type: c.property_type,
          filter_attributes: c.filter_attributes ?? {},
          min_area_sqm: c.min_area_sqm,
          max_area_sqm: c.max_area_sqm,
          min_price_minor: c.min_price_minor,
          max_price_minor: c.max_price_minor,
          currency: c.currency,
          active: true,
        })
      }
      load()
    } catch (e) {
      setError(String(e))
    }
  }

  // Диапазон цены для карточки: по локали, без нулевых долей («200 000 –
  // 800 000 CZK»). Формы при этом по-прежнему используют fromMinor — там
  // «200000.00» корректный формат ввода.
  const priceRangeText = (c: SearchConfig): string | null => {
    if (c.min_price_minor === null || c.max_price_minor === null) return null
    const code = c.currency ?? meta.market_currencies[c.country]
    const exp = meta.currencies.find((x) => x.code === code)?.exponent ?? 2
    const f = (m: number) =>
      new Intl.NumberFormat(locale, { maximumFractionDigits: exp }).format(m / 10 ** exp)
    return `${f(c.min_price_minor)} – ${f(c.max_price_minor)} ${code}`
  }

  // U23: значение фильтра в карточке — без машинной формы: bool → да/нет.
  const filterValue = (country: string, key: string, v: unknown): string => {
    const def = attrsByCountry[country]?.find((d) => d.key === key)
    if (def?.data_type === 'bool') return v === true ? t('common.yes') : t('common.no')
    return String(v)
  }

  // Поля формы — общие для новой записи и правки (U22). Вычисляются
  // только когда модалка открыта: на первом рендере modal = null, и
  // обращение modal!.form.* бросит «Cannot read properties of null».
  const formFields = modal ? (
    <>
      <div className="form-grid">
        <label>
          {t('search.country')}
          <select
            value={modal!.form.country}
            onChange={(e) => set({ country: e.target.value, filters: {} })}
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
            value={modal!.form.deal_type}
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
            value={modal!.form.property_type}
            placeholder="house / apartment / …"
            onChange={(e) => set({ property_type: e.target.value })}
          />
        </label>
        <label>
          {t('search.price_min', { currency: marketCurrency })}
          <input
            inputMode="decimal"
            value={modal!.form.min_price}
            onChange={(e) => set({ min_price: e.target.value })}
          />
        </label>
        <label>
          {t('search.price_max', { currency: marketCurrency })}
          <input
            inputMode="decimal"
            value={modal!.form.max_price}
            onChange={(e) => set({ max_price: e.target.value })}
          />
        </label>
        <label>
          {t('search.area_min')}
          <input
            inputMode="decimal"
            value={modal!.form.min_area}
            onChange={(e) => set({ min_area: e.target.value })}
          />
        </label>
        <label>
          {t('search.area_max')}
          <input
            inputMode="decimal"
            value={modal!.form.max_area}
            onChange={(e) => set({ max_area: e.target.value })}
          />
        </label>
        <label className="checkbox">
          <input
            type="checkbox"
            checked={modal!.form.active}
            onChange={(e) => set({ active: e.target.checked })}
          />
          <span>{t('search.active')}</span>
        </label>
      </div>

      <h4>{t('search.filters')}</h4>
      {attrs.length === 0 ? (
        <p className="muted">{t('search.no_filters')}</p>
      ) : (
        <div className="form-grid">
          {attrs.map((d) => (
            <div key={d.key} className="attr-field">
              {d.data_type === 'bool' ? (
                <label className="checkbox">
                  <input
                    type="checkbox"
                    checked={modal!.form.filters[d.key] === 'true'}
                    onChange={(e) =>
                      set({
                        filters: {
                          ...modal!.form.filters,
                          [d.key]: e.target.checked ? 'true' : '',
                        },
                      })
                    }
                  />
                  <span>{labelOf(d)}</span>
                </label>
              ) : d.data_type === 'enum' ? (
                <label>
                  {labelOf(d)}
                  <select
                    value={modal!.form.filters[d.key] ?? ''}
                    onChange={(e) =>
                      set({ filters: { ...modal!.form.filters, [d.key]: e.target.value } })
                    }
                  >
                    <option value="">—</option>
                    {(d.allowed_values ?? []).map((v) => (
                      <option key={v} value={v}>
                        {v}
                      </option>
                    ))}
                  </select>
                </label>
              ) : (
                <label>
                  {labelOf(d)}
                  <input
                    type="number"
                    step={d.data_type === 'int' ? 1 : 'any'}
                    inputMode={d.data_type === 'int' ? 'numeric' : 'decimal'}
                    value={modal!.form.filters[d.key] ?? ''}
                    onChange={(e) =>
                      set({ filters: { ...modal!.form.filters, [d.key]: e.target.value } })
                    }
                  />
                </label>
              )}
              {/* U20: происхождение значения — видимой строкой под полем,
                  не только в title= (смысл не в tooltip-ах). */}
              {d.source_evidence && <span className="attr-evidence">{d.source_evidence}</span>}
            </div>
          ))}
        </div>
      )}
    </>
  ) : null

  return (
    <>
      <div className="toolbar">
        {/* TZ_UI v5 U22: «Новая конфигурация» — модалка, а не форма
            в конце списка (D6: правка только в модалке). */}
        <button className="ghost" onClick={openNew}>
          {t('search.new')}
        </button>
      </div>
      {savedMsg && (
        <p className="form-note" role="status">
          {savedMsg}
        </p>
      )}
      {error && <div className="error">{error}</div>}
      {configs && configs.length === 0 && <p className="muted">{t('search.no_configs')}</p>}
      {configs && configs.length > 0 && (
        <div className="list">
          {configs.map((c) => {
            const priceRange = priceRangeText(c)
            const filters = Object.entries(c.filter_attributes ?? {})
            const hasArea = c.min_area_sqm != null && c.max_area_sqm != null
            const hasConditions =
              c.min_price_minor !== null || hasArea || filters.length > 0
            return (
              <article
                className={'item' + (highlightId === c.id ? ' highlight' : '')}
                key={c.id}
              >
                <div className="cfg-head">
                  <div className="cfg-title">
                    <h3 className="item-title">
                      {t(`countries.${c.country}`, { defaultValue: c.country })} —{' '}
                      {t(`search.deal_${c.deal_type}`, { defaultValue: c.deal_type })}
                      {c.property_type ? `, ${c.property_type}` : ''}
                    </h3>
                    {c.active ? (
                      <span className="tag">{t('search.on')}</span>
                    ) : (
                      <span className="tag off">{t('search.off')}</span>
                    )}
                  </div>
                  {/* U23: подзаголовок — структурированными строками
                      (цена / источник / обновлена), не одной строкой через запятую */}
                  <p className="item-sub">
                    {priceRange && <span className="num">{priceRange}</span>}
                    <span>
                      {t('search.source')}:{' '}
                      {c.source_id ? (
                        c.source_id
                      ) : (
                        <span className="warn-text">{t('search.source_none')}</span>
                      )}
                    </span>
                    <span>
                      {t('search.updated')}: {formatDate(c.updated_at, locale)}
                    </span>
                  </p>
                </div>

                {(hasArea || filters.length > 0) && (
                  <dl className="kv">
                    {hasArea && (
                      <>
                        <dt>{t('search.area_range')}</dt>
                        <dd className="num">
                          {`${c.min_area_sqm}–${c.max_area_sqm} ${t('search.sqm')}`}
                        </dd>
                      </>
                    )}
                    {filters.length > 0 && (
                      <>
                        <dt>{t('search.filters')}</dt>
                        {/* U23: фильтр — по одному на строку, подпись из
                            реестра страны, не машинный ключ (D3) */}
                        <dd>
                          <ul className="filter-lines">
                            {filters.map(([k, v]) => (
                              <li key={k} className="num">
                                {labelOfByCountry(c.country, k)}: {filterValue(c.country, k, v)}
                              </li>
                            ))}
                          </ul>
                        </dd>
                      </>
                    )}
                  </dl>
                )}
                {!hasConditions && (
                  <p className="item-sub">{t('search.no_conditions')}</p>
                )}

                <div className="cfg-actions">
                  <button className="ghost" onClick={() => openEdit(c)}>
                    {t('search.edit')}
                  </button>
                  <button
                    className={c.active ? 'ghost danger' : 'ghost'}
                    onClick={() => void toggle(c)}
                  >
                    {c.active ? t('search.deactivate') : t('search.enable')}
                  </button>
                </div>
              </article>
            )
          })}
        </div>
      )}

      {/* TZ_UI v5 U22: правка/создание — только в модалке (U07):
          фокус, Escape, возврат фокуса; фон — отдельный слой */}
      {modal && (
        <>
          <div className="modal-backdrop" onClick={requestClose} />
          <Dialog
            open
            label={modal.baseline ? t('search.modal_edit') : t('search.new')}
            onClose={requestClose}
            className="modal"
          >
            <div className="modal-head">
              <h3>{modal.baseline ? t('search.modal_edit') : t('search.new')}</h3>
              <button
                type="button"
                className="modal-x"
                aria-label={t('common.close')}
                onClick={requestClose}
              >
                ×
              </button>
            </div>
            {modalMode === 'form' && (
              <form
                className="card"
                noValidate
                onSubmit={(e) => {
                  e.preventDefault()
                  void onSave()
                }}
              >
                {formFields}
                <div className="form-actions">
                  <button type="submit" className="primary">
                    {t('search.save')}
                  </button>
                  <button type="button" className="ghost" onClick={requestClose}>
                    {t('search.cancel')}
                  </button>
                </div>
              </form>
            )}
            {modalMode === 'diff' && (
              <div className="card">
                <h3>{t('search.diff_title')}</h3>
                {/* Подтверждение — именно из этого списка: пользователь видит
                    каждое «было → стало» до применения (ТЗ v4 §7.1) */}
                <dl className="diff-list">
                  {diffRows.map((r) => (
                    <Fragment key={r.label}>
                      <dt>{r.label}</dt>
                      <dd className="num">
                        {r.was} → {r.now}
                      </dd>
                    </Fragment>
                  ))}
                </dl>
                <div className="form-actions">
                  <button
                    type="button"
                    className="primary"
                    onClick={() => {
                      const built = buildInput(modal.form)
                      if ('error' in built) {
                        setError(built.error)
                        setModalMode('form')
                        return
                      }
                      void apply(built, modal.form)
                    }}
                  >
                    {t('search.apply')}
                  </button>
                  <button
                    type="button"
                    className="ghost"
                    onClick={() => {
                      setModalMode('form')
                      setDiffRows([])
                    }}
                  >
                    {t('search.back_edit')}
                  </button>
                </div>
              </div>
            )}
            {modalMode === 'confirm-close' && (
              <div className="card">
                <h3>{t('search.close_title')}</h3>
                <p>{t('search.close_text')}</p>
                <div className="form-actions">
                  <button type="button" className="primary" onClick={closeModal}>
                    {t('common.close')}
                  </button>
                  <button type="button" className="ghost" onClick={() => setModalMode('form')}>
                    {t('search.stay_btn')}
                  </button>
                </div>
              </div>
            )}
          </Dialog>
        </>
      )}
    </>
  )
}
