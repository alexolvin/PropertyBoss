// resize.go — уменьшение изображения (билинейная фильтрация).
//
// Увеличение запрещено: оригинал уже не шире целевой ширины — отдаём как
// есть (апскейл деградирует качество и растит вес без пользы).
package photos

import (
	"image"
	"image/color"
)

// rgba4 — premultiplied-RGBA, 16-битные компоненты (как их отдаёт
// color.Color.RGBA()).
type rgba4 struct{ r, g, b, a uint32 }

func (c rgba4) color() color.RGBA64 {
	return color.RGBA64{
		R: uint16(c.r), G: uint16(c.g),
		B: uint16(c.b), A: uint16(c.a),
	}
}

// resizeBilinear — src → изображение ширины w (высота пропорционально).
// src уже не шире w — src без изменений.
func resizeBilinear(src image.Image, w int) image.Image {
	b := src.Bounds()
	if b.Dx() <= w {
		return src
	}
	h := b.Dy() * w / b.Dx()
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	rx := float64(b.Dx()) / float64(w)
	ry := float64(b.Dy()) / float64(h)
	for y := 0; y < h; y++ {
		sy := (float64(y)+0.5)*ry - 0.5
		y0, y1, fy := clampNei(sy, b.Dy())
		for x := 0; x < w; x++ {
			sx := (float64(x)+0.5)*rx - 0.5
			x0, x1, fx := clampNei(sx, b.Dx())

			p00 := rgba4From(src, b, x0, y0)
			p10 := rgba4From(src, b, x1, y0)
			p01 := rgba4From(src, b, x0, y1)
			p11 := rgba4From(src, b, x1, y1)
			dst.Set(x, y, mix(p00, p10, p01, p11, fx, fy).color())
		}
	}
	return dst
}

func rgba4From(src image.Image, b image.Rectangle, x, y int) rgba4 {
	r, g, bl, a := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
	return rgba4{r: r, g: g, b: bl, a: a}
}

// mix — билинейная интерполяция premultiplied-RGBA:
// сначала по x (top/bottom), затем по y.
func mix(p00, p10, p01, p11 rgba4, fx, fy float64) rgba4 {
	top := lerp(p00, p10, fx)
	bot := lerp(p01, p11, fx)
	return lerp(top, bot, fy)
}

func lerp(a, b rgba4, t float64) rgba4 {
	return rgba4{
		r: mixF(a.r, b.r, t),
		g: mixF(a.g, b.g, t),
		b: mixF(a.b, b.b, t),
		a: mixF(a.a, b.a, t),
	}
}

// mixF — интерполяция одной 16-битной premultiplied-компоненты.
func mixF(av, bv uint32, t float64) uint32 {
	v := float64(av) + (float64(bv)-float64(av))*t
	if v < 0 {
		v = 0
	}
	if v > 65535 {
		v = 65535
	}
	return uint32(v)
}

// clampNei — sx ∈ [0, dx) → (x0, x1, доля x1); края упираются в границу.
func clampNei(sx float64, dx int) (int, int, float64) {
	x0 := int(sx)
	fx := sx - float64(x0)
	if x0 < 0 {
		x0, fx = 0, 0
	}
	if x0 >= dx {
		x0 = dx - 1
	}
	x1 := x0 + 1
	if x1 >= dx {
		x1 = dx - 1
		fx = 0
	}
	return x0, x1, fx
}
