// TZ UI v5 U12: генератор PWA-иконок без внешних зависимостей.
// Чистый Node (zlib deflateSync + ручной CRC32) — PNG кодируется вручную,
// чтобы не тянуть canvas/sharp в devDeps ради трёх статичных файлов.
// Запуск: node scripts/gen-icons.mjs → public/icons/*.png (коммитить).
// Маска: дом на акцентной плитке (цвет :root, styles.css) — то же, что
// public/icons/icon.svg, но растровое, 192/512 + apple-touch-icon 180.
import { deflateSync } from 'node:zlib'
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..', 'public', 'icons')
mkdirSync(root, { recursive: true })

const ACCENT = [0x17, 0x72, 0x45] // #177245
const WHITE = [0xff, 0xff, 0xff]

// Таблица CRC32 (IEEE 802.3, полином 0xedb88320 в отражённой форме).
const CRC_TABLE = new Int32Array(256)
for (let n = 0; n < 256; n++) {
  let c = n
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1
  CRC_TABLE[n] = c
}
function crc32(buf) {
  let c = 0xffffffff
  for (const b of buf) c = CRC_TABLE[(c ^ b) & 0xff] ^ (c >>> 8)
  return (c ^ 0xffffffff) >>> 0
}
function chunk(type, data) {
  const len = Buffer.alloc(4)
  len.writeUInt32BE(data.length)
  const typeBuf = Buffer.from(type, 'ascii')
  const crc = Buffer.alloc(4)
  crc.writeUInt32BE(crc32(Buffer.concat([typeBuf, data])))
  return Buffer.concat([len, typeBuf, data, crc])
}
// PNG: сигнатура + IHDR + IDAT(дефлированные raw-сканлайны, фильтр 0) + IEND.
function png(size, pixelAt) {
  const stride = size * 4 + 1
  const raw = Buffer.alloc(size * stride)
  for (let y = 0; y < size; y++) {
    raw[y * stride] = 0 // фильтр None
    for (let x = 0; x < size; x++) {
      const [r, g, b, a] = pixelAt(x, y)
      const o = y * stride + 1 + x * 4
      raw[o] = r
      raw[o + 1] = g
      raw[o + 2] = b
      raw[o + 3] = a
    }
  }
  const ihdr = Buffer.alloc(13)
  ihdr.writeUInt32BE(size, 0)
  ihdr.writeUInt32BE(size, 4)
  ihdr[8] = 8 // битная глубина
  ihdr[9] = 6 // color type RGBA
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(raw, { level: 9 })),
    chunk('IEND', Buffer.alloc(0)),
  ])
}

// Дом в нормализованных координатах (0..1): крыша-треугольник с вершиной
// (0.5, 0.2) и свесами до 0.25/0.75 при v=0.45; корпус 0.25..0.75 × 0.45..0.8;
// дверь-вырез 0.45..0.55 × 0.6..0.8 (плитка проступает).
function housePixel(x, y, size) {
  const u = (x + 0.5) / size
  const v = (y + 0.5) / size
  const inRoof = v >= 0.2 && v <= 0.45 && Math.abs(u - 0.5) <= v - 0.2
  const inBody = u >= 0.25 && u <= 0.75 && v >= 0.45 && v <= 0.8
  const inDoor = u >= 0.45 && u <= 0.55 && v >= 0.6 && v <= 0.8
  if (inRoof || (inBody && !inDoor)) return [...WHITE, 255]
  return [...ACCENT, 255]
}

for (const size of [192, 512, 180]) {
  const name = size === 180 ? 'apple-touch-icon.png' : `icon-${size}.png`
  writeFileSync(join(root, name), png(size, (x, y) => housePixel(x, y, size)))
  console.log(`public/icons/${name} (${size}×${size})`)
}
