// photos — скачивание и ресайз фотографий объявлений (ТЗ v4 §4.4).
//
// Каждый фото хранится как оригинальная ссылка + (для первых max_local)
// локальные WebP-копии в двух размерах: карта-карточка (больше) и список
// (меньше). Порядок фотографий сохраняется в опубликованном.
//
// ПРАВОВОЕ ОГРАНИЧЕНИЕ (ТЗ v4 §4.4, ТЗ v2 §13): изображения защищены
// авторским правом площадок. Хранение и показ — только владельцу системы
// в личных целях; публикация и передача третьим лицам не предусмотрены.
//
// Вежливость к серверу изображений: минимальный интервал между запросами
// (page_delay_ms) + ограниченное число параллельных загрузчиков.
package photos

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/chai2010/webp"
	"golang.org/x/sync/errgroup"

	"propertyboss/internal/config"
)

// photoFetchLimit — максимум, читаемый с одного запроса оригинала.
const photoFetchLimit = 10 << 20

// userAgent — описательный UA (как в коннекторе bazos).
const userAgent = "PropertyBoss/1.0 (personal research project)"

// Photo — результат по одной фотографии объекта.
// FileCard/FileList — относительные пути от корня cfg.Dir
// ("" — локальной копии нет: оригинальная ссылка сохраняется, а в
// Error — причина, если она была).
type Photo struct {
	Ord         int    `json:"ord"`
	OriginalURL string `json:"original_url"`
	FileCard    string `json:"file_card"`
	BytesCard   int    `json:"bytes_card"`
	FileList    string `json:"file_list"`
	BytesList   int    `json:"bytes_list"`
	Error       string `json:"error"`
	// Permanent — повторная загрузка бессмысленна (мёртвая ссылка 4xx,
	// битый файл, весовой лимит): строка object_photos помечается
	// download_error и не ретраится.
	Permanent bool `json:"permanent,omitempty"`
}

// Job — одна фотография объявления: ord (позиция в галерее, 1 —
// обложка) и оригинальный URL.
type Job struct {
	Ord int
	URL string
}

// throttle — минимальный интервал между запросами (вежливость).
type throttle struct {
	mu       sync.Mutex
	last     time.Time
	interval time.Duration
}

func (t *throttle) wait(ctx context.Context) error {
	if t.interval <= 0 {
		return nil
	}
	t.mu.Lock()
	now := time.Now()
	wait := t.last.Add(t.interval).Sub(now)
	if wait > 0 {
		t.last = now.Add(wait)
	} else {
		t.last = now
	}
	t.mu.Unlock()
	if wait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil
}

// Process — скачать и пересохратить фотографии объекта (ТЗ v4 §4.4).
//
//	cfg  — секция photos;
//	objID — id объекта (каталог cfg.Dir/<objID>);
//	urls  — фотографии в опубликованном порядке (дубликаты уже сняты
//	       парсером); ord = 1..len(urls).
//
// Первые max_local фото — локально в двух размерах, остальные — только
// оригинальные ссылки. Ошибка отдельного фото не останавливает прогон:
// OriginalURL сохраняется, причина — в Photo.Error. Возврат error —
// только на системные сбои (каталог, ctx).
func Process(ctx context.Context, cfg config.PhotosConf, objID int64, urls []string) ([]Photo, error) {
	jobs := make([]Job, len(urls))
	for i, u := range urls {
		jobs[i] = Job{Ord: i + 1, URL: u}
	}
	return ProcessObject(ctx, cfg, objID, jobs,
		&throttle{interval: time.Duration(cfg.PageDelayMS) * time.Millisecond})
}

// ProcessObject — то же, что Process, но по явному набору фотографий
// (ord может быть не сплошным — ретрай отдельных строк object_photos).
// thr — общий ограничитель интервала (вежливость между объектами);
// nil — без пауз.
func ProcessObject(ctx context.Context, cfg config.PhotosConf, objID int64, jobs []Job, thr *throttle) ([]Photo, error) {
	objDir := filepath.Join(cfg.Dir, strconv.FormatInt(objID, 10))
	if err := os.MkdirAll(objDir, 0o755); err != nil {
		return nil, fmt.Errorf("photos: каталог %s: %w", objDir, err)
	}
	if thr == nil {
		thr = &throttle{}
	}

	out := make([]Photo, len(jobs))
	httpc := &http.Client{Timeout: time.Duration(cfg.TimeoutSec) * time.Second}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(cfg.Workers)
	for i, j := range jobs {
		i, j := i, j
		g.Go(func() error {
			out[i] = Photo{Ord: j.Ord, OriginalURL: j.URL}
			if j.Ord > cfg.MaxLocal {
				return nil // дальше — только ссылки (ТЗ v4 §4.4: max_local)
			}
			one := processOne(gctx, cfg, httpc, thr, objDir, j.Ord, j.URL)
			out[i].FileCard, out[i].BytesCard, out[i].FileList, out[i].BytesList, out[i].Error, out[i].Permanent =
				one.FileCard, one.BytesCard, one.FileList, one.BytesList, one.Error, one.Permanent
			return nil // ошибка фото — в Photo.Error, не останавливает прогон
		})
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("photos: %w", err)
	}
	return out, nil
}

// processOne — одна фотография: скачать, декодировать, два размера.
// Ошибка — в p.Error; бессмысленный для повтора сбой (мёртвая ссылка,
// битый файл, весовой лимит) дополнительно — p.Permanent.
func processOne(ctx context.Context, cfg config.PhotosConf, httpc *http.Client, thr *throttle, objDir string, ord int, url string) (p Photo) {
	if err := thr.wait(ctx); err != nil {
		p.Error = err.Error() // ctx: ретрай имеет смысл
		return
	}
	data, status, err := fetch(ctx, httpc, url)
	if err != nil {
		p.Error = err.Error()
		// Мёртвая ссылка (4xx) не оживёт; 5xx/сеть — временные,
		// повторится следующим прогоном.
		p.Permanent = status >= 400 && status < 500
		return
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		p.Error = fmt.Sprintf("декодирование: %v", err)
		p.Permanent = true // тот же файл → тот же результат
		return
	}

	card := resizeBilinear(img, cfg.CardWidth)
	list := resizeBilinear(img, cfg.ListWidth)

	cardBytes, err := encodeWithin(card, cfg.Quality, cfg.MaxBytes)
	if err == nil {
		p.FileCard = relCardName(objDir, ord, "card")
		p.BytesCard = len(cardBytes)
		if err := writeLocal(cfg.Dir, p.FileCard, cardBytes); err != nil {
			p.FileCard, p.BytesCard = "", 0
			p.Error = err.Error()
			return // диск: ретрай имеет смысл
		}
	} else {
		p.Error = err.Error()
		p.Permanent = true // лимит/кодировщик: повтор бесполезен
	}

	listBytes, err := encodeWithin(list, cfg.Quality, cfg.MaxBytes)
	if err == nil {
		p.FileList = relCardName(objDir, ord, "list")
		p.BytesList = len(listBytes)
		if werr := writeLocal(cfg.Dir, p.FileList, listBytes); werr != nil {
			p.FileList, p.BytesList = "", 0
			if p.Error == "" {
				p.Error = werr.Error()
			}
			return
		}
	} else if p.Error == "" {
		p.Error = err.Error()
		p.Permanent = true
	}
	return
}

// relCardName — относительный путь файла: <objID>/<NN>_card.webp.
func relCardName(objDir string, ord int, kind string) string {
	return filepath.Base(objDir) + string(os.PathSeparator) +
		strconv.FormatInt(int64(ord), 10) + "_" + kind + ".webp"
}

// writeLocal — запись в cfg.Dir/<rel> (rel — относительный путь).
func writeLocal(dir, rel string, data []byte) error {
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("photos: каталог %s: %w", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return fmt.Errorf("photos: запись %s: %w", rel, err)
	}
	return nil
}

// fetch — GET оригинала с ограничением размера. status — HTTP-код
// (0 — ответа не было); не-200 — ошибка с кодом (4xx — мёртвая ссылка).
func fetch(ctx context.Context, httpc *http.Client, url string) (data []byte, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("запрос: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("сеть: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, photoFetchLimit))
	if err != nil {
		return nil, 0, fmt.Errorf("чтение: %w", err)
	}
	return data, resp.StatusCode, nil
}

// encodeWithin — WebP-кодирование в пределах лимита: качество из конфига,
// при превышении — понижение (70 → 50 → 30). Лимит недостижим — ошибка
// (копию не храним, оригинальная ссылка остаётся — честнее превысить
// «нет копии», чем «слишком тяжёлая копия»).
func encodeWithin(img image.Image, quality, maxBytes int) ([]byte, error) {
	var last int
	for _, q := range qualityLadder(quality) {
		var buf bytes.Buffer
		if err := webp.Encode(&buf, img, &webp.Options{Quality: float32(q)}); err != nil {
			return nil, fmt.Errorf("webp: %w", err)
		}
		last = buf.Len()
		if buf.Len() <= maxBytes {
			return buf.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("webp %d байт > лимита %d", last, maxBytes)
}

// qualityLadder — стартовое качество + фиксированные ступени понижения.
func qualityLadder(q int) []int {
	out := []int{q}
	for _, s := range []int{70, 50, 30} {
		if s < q {
			out = append(out, s)
		}
	}
	return out
}
