// Тесты photos: ресайз, Process на mock-сервере, весовой лимит.
package photos

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"propertyboss/internal/config"
)

// gradientPNG — синтетическое «фото» w×h: градиент (кодируется webp).
func gradientPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(x * 255 / w),
				G: uint8(y * 255 / h),
				B: uint8((x + y) * 255 / (w + h)),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// photoServer — отдаёт PNG на /img/<n>; 404-список — «сломанные».
func photoServer(t *testing.T, broken map[string]bool) *httptest.Server {
	t.Helper()
	img := gradientPNG(400, 300)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broken[r.URL.Path] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(img)
	}))
}

func testPhotosCfg(dir string) config.PhotosConf {
	return config.PhotosConf{
		Dir:         dir,
		CardWidth:   320,
		ListWidth:   160,
		Quality:     82,
		MaxBytes:    1 << 20,
		MaxLocal:    2,
		Workers:     2,
		TimeoutSec:  10,
		PageDelayMS: 0,
	}
}

func TestResizeBilinear(t *testing.T) {
	src := gradientImg(800, 600)
	dst := resizeBilinear(src, 200)
	b := dst.Bounds()
	if b.Dx() != 200 || b.Dy() != 150 {
		t.Errorf("800×600 → 200: получено %dx%d, ожидалось 200×150", b.Dx(), b.Dy())
	}
	// Увеличение запрещено: маленькое — как есть.
	small := gradientImg(100, 50)
	if dst2 := resizeBilinear(small, 200); dst2.Bounds().Dx() != 100 {
		t.Errorf("100 → 200: ширина %d, ожидалось 100 (апскейл запрещён)", dst2.Bounds().Dx())
	}
}

func gradientImg(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	return img
}

// isWebP — RIFF....WEBP-заголовок.
func isWebP(data []byte) bool {
	return len(data) >= 12 &&
		string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP"
}

func TestProcess(t *testing.T) {
	srv := photoServer(t, nil)
	defer srv.Close()
	dir := t.TempDir()
	cfg := testPhotosCfg(dir)

	urls := []string{
		srv.URL + "/img/1",
		srv.URL + "/img/2",
		srv.URL + "/img/3", // > max_local (2) — только ссылка
	}
	photos, err := Process(context.Background(), cfg, 42, urls)
	if err != nil {
		t.Fatal(err)
	}
	if len(photos) != 3 {
		t.Fatalf("len = %d, ожидалось 3", len(photos))
	}
	// Порядок сохранён.
	for i, p := range photos {
		if p.Ord != i+1 || p.OriginalURL != urls[i] {
			t.Errorf("photos[%d] = %+v (ord/URL сдвинулись)", i, p)
		}
	}
	// Первые две — локально в двух размерах.
	for _, i := range []int{0, 1} {
		p := photos[i]
		if p.FileCard == "" || p.BytesCard == 0 || p.FileList == "" || p.BytesList == 0 {
			t.Errorf("photos[%d]: локальные файлы не записаны: %+v", i, p)
			continue
		}
		for _, f := range []struct {
			rel  string
			want int
		}{
			{p.FileCard, p.BytesCard},
			{p.FileList, p.BytesList},
		} {
			data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.rel)))
			if err != nil {
				t.Errorf("%s: %v", f.rel, err)
				continue
			}
			if !isWebP(data) {
				t.Errorf("%s: не WebP (заголовок % x)", f.rel, data[:min(4, len(data))])
			}
			if len(data) != f.want {
				t.Errorf("%s: %d байт на диске, в Photo %d", f.rel, len(data), f.want)
			}
		}
	}
	// Третья — только ссылка.
	if p := photos[2]; p.FileCard != "" || p.FileList != "" {
		t.Errorf("photos[2] (за max_local): локальные файлы не должны быть: %+v", p)
	}
}

func TestProcessPhotoFailure(t *testing.T) {
	srv := photoServer(t, map[string]bool{"/img/2": true})
	defer srv.Close()
	dir := t.TempDir()
	cfg := testPhotosCfg(dir)
	cfg.MaxLocal = 3

	photos, err := Process(context.Background(), cfg, 7, []string{
		srv.URL + "/img/1",
		srv.URL + "/img/2", // 404
		srv.URL + "/img/3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if photos[0].Error != "" || photos[0].FileCard == "" {
		t.Errorf("photos[0] должно быть успешно: %+v", photos[0])
	}
	if photos[1].Error == "" || photos[1].FileCard != "" {
		t.Errorf("photos[1] (404): ожидалась Error без файлов, получено %+v", photos[1])
	}
	if photos[2].Error != "" {
		t.Errorf("photos[2] должно быть успешно: %+v", photos[2])
	}
}

// TestProcessObjectPermanent — 404 (мёртвая ссылка) помечается
// Permanent (строк не ретраится); ord не сплошной — порядок из задач.
func TestProcessObjectPermanent(t *testing.T) {
	srv := photoServer(t, map[string]bool{"/img/2": true})
	defer srv.Close()
	dir := t.TempDir()
	cfg := testPhotosCfg(dir)
	cfg.MaxLocal = 3

	photos, err := ProcessObject(context.Background(), cfg, 7, []Job{
		{Ord: 2, URL: srv.URL + "/img/2"}, // 404
		{Ord: 3, URL: srv.URL + "/img/3"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(photos) != 2 {
		t.Fatalf("len = %d, ожидалось 2", len(photos))
	}
	if p := photos[0]; p.Ord != 2 || p.Error == "" || !p.Permanent || p.FileCard != "" {
		t.Errorf("photos[0] (404): ожидался Permanent без файлов: %+v", p)
	}
	if p := photos[1]; p.Ord != 3 || p.Error != "" || p.FileCard == "" {
		t.Errorf("photos[1]: ожидался успех: %+v", p)
	}
}

func TestEncodeWithinLadder(t *testing.T) {
	img := gradientImg(640, 480)
	// Лимит достижим.
	b, err := encodeWithin(img, 82, 1<<20)
	if err != nil || len(b) > 1<<20 || !isWebP(b) {
		t.Errorf("encodeWithin: len=%d err=%v, ожидалось webp ≤ 1 МБ", len(b), err)
	}
	// Лимит недостижим (1 байт) — честная ошибка, а не превышение.
	if _, err := encodeWithin(img, 82, 1); err == nil {
		t.Error("encodeWithin(1 байт): ожидалась ошибка")
	}
}

func TestQualityLadder(t *testing.T) {
	if got := qualityLadder(82); len(got) != 4 || got[0] != 82 || got[3] != 30 {
		t.Errorf("qualityLadder(82) = %v", got)
	}
	// Стартовое ниже всех ступеней — только оно.
	if got := qualityLadder(20); len(got) != 1 || got[0] != 20 {
		t.Errorf("qualityLadder(20) = %v", got)
	}
}
