import type { DevClass } from './api'

export const DEV_SIGN: Record<DevClass, string> = {
  far_below: '▼▼',
  below: '▼',
  near: '●',
  above: '▲',
  far_above: '▲▲',
  none: '○',
}
