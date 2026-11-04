package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Секреты (database.dsn, telegram.token, api-ключи translate/extract) —
// отдельный файл secrets.yaml рядом с основным конфигом (решение
// оператора, 09.09: секрет не лежит в общем конфигурационном файле).
// Load подмешивает его поверх config.yaml; файл не в .gitignore —
// config/secrets.yaml.

// writeSecrets — пишет secrets.yaml в каталог cfgPath и возвращает путь.
func writeSecrets(t *testing.T, cfgPath, secrets string) string {
	t.Helper()
	sp := SecretsPath(cfgPath)
	if err := os.WriteFile(sp, []byte(secrets), 0o600); err != nil {
		t.Fatal(err)
	}
	return sp
}

// writeBase — пишет config.yaml (cfgBase с подстановкой) и возвращает путь.
func writeBase(t *testing.T, dir, base string) string {
	t.Helper()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const baseDSN = `dsn: "postgres://u:p@127.0.0.1:5432/db?sslmode=disable"`

func TestLoadSecretsMerge(t *testing.T) {
	dir := t.TempDir()
	// Общий конфиг без секретов: dsn пуст, telegram включён без токена.
	// Без secrets.yaml такой конфиг Load отклонит (валидация) — с ним
	// обязан собрать.
	base := strings.Replace(cfgBase, baseDSN, `dsn: ""`, 1)
	base = strings.Replace(base, "telegram:\n  enabled: false",
		"telegram:\n  enabled: true\n  chat_id: \"-100/1\"", 1)
	cfgPath := writeBase(t, dir, base)
	writeSecrets(t, cfgPath, `
database:
  dsn: "postgres://sec:ret@127.0.0.1:5432/db?sslmode=disable"
telegram:
  token: "123456:SECRET-TOKEN"
`)
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v, ждали успех (секреты из secrets.yaml)", err)
	}
	if cfg.Database.DSN != "postgres://sec:ret@127.0.0.1:5432/db?sslmode=disable" {
		t.Errorf("dsn = %q, ждали значение из secrets.yaml", cfg.Database.DSN)
	}
	if cfg.Telegram.Token != "123456:SECRET-TOKEN" {
		t.Errorf("token = %q, ждали значение из secrets.yaml", cfg.Telegram.Token)
	}
}

func TestLoadSecretsPrecedence(t *testing.T) {
	dir := t.TempDir()
	// Секрет задан в обоих местах — побеждает secrets.yaml.
	base := strings.Replace(cfgBase, "telegram:\n  enabled: false",
		"telegram:\n  enabled: true\n  token: \"base-token\"\n  chat_id: \"-100/1\"", 1)
	cfgPath := writeBase(t, dir, base)
	writeSecrets(t, cfgPath, "telegram:\n  token: \"secrets-token\"\n")
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Telegram.Token != "secrets-token" {
		t.Errorf("token = %q, ждали %q (secrets.yaml имеет приоритет)", cfg.Telegram.Token, "secrets-token")
	}
}

func TestLoadWithoutSecretsFile(t *testing.T) {
	dir := t.TempDir()
	// Обратная совместимость: секреты в основном конфиге, secrets.yaml
	// нет — Load обязан их принять.
	base := strings.Replace(cfgBase, "telegram:\n  enabled: false",
		"telegram:\n  enabled: true\n  token: \"in-base\"\n  chat_id: \"-100/1\"", 1)
	cfgPath := writeBase(t, dir, base)
	if _, err := os.Stat(SecretsPath(cfgPath)); !os.IsNotExist(err) {
		t.Fatalf("secrets.yaml не должен существовать: %v", err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v, ждали успех (секреты в основном конфиге)", err)
	}
	if cfg.Telegram.Token != "in-base" {
		t.Errorf("token = %q, ждали %q", cfg.Telegram.Token, "in-base")
	}
	if cfg.Database.DSN != "postgres://u:p@127.0.0.1:5432/db?sslmode=disable" {
		t.Errorf("dsn = %q, ждали значение из config.yaml", cfg.Database.DSN)
	}
}

func TestLoadSecretsAPIKeys(t *testing.T) {
	dir := t.TempDir()
	// Ключи LLM — в secrets.yaml, остальное (модель, base_url) — в общем
	// конфиге: блоки translate/extract валидны только в сумме.
	base := cfgBase + `
translate:
  base_url: "https://api.openai.com/v1"
  model: "m-translate"
extract:
  base_url: "https://api.openai.com/v1"
  model: "m-extract"
  max_confidence: 0.9
  attr_list_version: "cz-test-v1"
`
	cfgPath := writeBase(t, dir, base)
	writeSecrets(t, cfgPath, `
translate:
  api_key: "sk-translate"
extract:
  api_key: "sk-extract"
`)
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Translate.APIKey != "sk-translate" {
		t.Errorf("translate.api_key = %q, ждали %q", cfg.Translate.APIKey, "sk-translate")
	}
	if cfg.Extract.APIKey != "sk-extract" {
		t.Errorf("extract.api_key = %q, ждали %q", cfg.Extract.APIKey, "sk-extract")
	}
	if cfg.Translate.Model != "m-translate" || cfg.Extract.Model != "m-extract" {
		t.Errorf("модели из общего конфига потеряны: %q / %q", cfg.Translate.Model, cfg.Extract.Model)
	}
}

func TestLoadSecretsMalformed(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeBase(t, dir, cfgBase)
	writeSecrets(t, cfgPath, "telegram: [нестроеный yaml\n")
	_, err := Load(cfgPath)
	if err == nil || !strings.Contains(err.Error(), "секретов") {
		t.Fatalf("Load: %v, ждали ошибку разбора secrets.yaml", err)
	}
}
