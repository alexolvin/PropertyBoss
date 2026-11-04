package scan

import (
	"errors"
	"fmt"
	"net/url"
)

// FailureKind — тип неудачи скана (scan_runs.failure_kind, ТЗ §8.2.2).
type FailureKind string

const (
	FailCaptcha      FailureKind = "captcha"
	FailHTTP429      FailureKind = "http_429"
	FailLayoutChange FailureKind = "layout_change"
	FailNetwork      FailureKind = "network"
)

// Частые причины, которые коннектор оборачивает в Fail:
var (
	ErrCaptcha      = errors.New("капча или блокировка: источник вернул проверку, а не выдачу")
	ErrHTTP429      = errors.New("http 429: источник ограничил частоту запросов")
	ErrLayoutChange = errors.New("layout change: страница не распознана (верстка изменилась?)")
)

// Fail — ошибка скана с классифицируемым типом.
type Fail struct {
	Kind FailureKind
	Err  error
}

func (f *Fail) Error() string { return string(f.Kind) + ": " + f.Err.Error() }
func (f *Fail) Unwrap() error { return f.Err }

// NewFail — ошибка скана с типом неудачи:
// NewFail(FailHTTP429, "страница 3 из 5").
func NewFail(kind FailureKind, err error) error { return &Fail{Kind: kind, Err: err} }

// NewFailf — NewFail с форматированием сообщения.
func NewFailf(kind FailureKind, format string, args ...any) error {
	return &Fail{Kind: kind, Err: fmt.Errorf(format, args...)}
}

// Classify — тип неудачи для ошибки; пустой Kind, если тип не определён
// (failure_kind в scan_runs останется NULL).
// Сетевые ошибки (url.Error: таймауты, разрыв соединения, DNS) — 'network'.
func Classify(err error) FailureKind {
	var f *Fail
	if errors.As(err, &f) {
		return f.Kind
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return FailNetwork
	}
	return ""
}
