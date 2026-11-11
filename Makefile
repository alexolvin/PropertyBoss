# PropertyBoss — путь выпуска раздачи (TZ UI v5 U03).
#
# make ui — единственный путь выпуска UI:
#   1. bin/pb — собирается заново (в нём чек web из U04; бинарь
#      старше кода чека чекнуть не может);
#   2. web: npm ci + npm run build (ручной npm run build из
#      процедуры выпуска исключён — только через make ui);
#   3. чек web (U04): бандл в web/dist собран из HEAD и не старше
#      файлов web/src;
#   4. прогон тестов §10 (Playwright + axe-core) — против одноразового
#      serve на 127.0.0.1:8091 из свежего web/dist (см. ниже).
#
# Прогон §10 идёт против одноразового, а не operator-serve: gate
# должен проверять ТОЛЬКО что собранный бандл, а не состояние
# long-running-процесса. Одноразовый serve:
#   - tmp-конфиг = config.yaml с dashboard.listen = 127.0.0.1:8091
#     (единственный изменённый ключ; файл и лог — tmp_*, gitignored);
#     лежит в config/, а не в корне: DSN живёт в secrets.yaml рядом с
#     конфигом (internal/config: SecretsPath) — в корне секреты не
#     находятся и serve не поднимается (D25);
#   - read-only: `pb serve` — только API-сервер, без планировщика
#     сканов и notify-flush (cmd/pb/main.go runServe);
#   - живёт до конца прогона тестов, затем kill по PID;
#   - долгоживущий serve оператора (адрес из config.yaml, по умолчанию
#     100.95.195.40:8091) от прогона не зависит.
#
# Историю отсрочки прогона до Волны 2 см. в git-log: инвариант
# «нет обрезки nowrap» (U41.3) ловил E9, чей фикс (U16) стоял в Волне 2.
#
# «Дерево с изменённым web/src без пересборки → код 1» держит чек web:
# make ui всегда пересобирает, поэтому на выходе он даёт только свежий
# бандл; чек страхует несостоявшуюся или несогласованную сборку и
# работает отдельно: bin/pb doctor web.

.PHONY: ui ui-live

# config/, а не корень: SecretsPath ищет secrets.yaml рядом с конфигом (D25)
UI_GATE_CFG = config/tmp_ui_gate_config.yaml
UI_GATE_URL = http://127.0.0.1:8091

ui:
	go build -o bin/pb ./cmd/pb
	cd web && npm ci && npm run build
	bin/pb doctor web
	@sed '/^  listen:/s|.*|  listen: "127.0.0.1:8091"|' config/config.yaml > $(UI_GATE_CFG)
	@(PB_CONFIG=$(UI_GATE_CFG) nohup bin/pb serve > tmp_ui_gate_serve.log 2>&1 & echo $$! > tmp_ui_gate_serve.pid)
	@ok=0; for i in $$(seq 1 40); do curl -sf $(UI_GATE_URL)/api/meta > /dev/null 2>&1 && { ok=1; break; }; sleep 0.5; done; \
	if [ "$$ok" -ne 1 ]; then \
	  echo "make ui: одноразовый serve не поднялся (tmp_ui_gate_serve.log):"; cat tmp_ui_gate_serve.log; \
	  kill "$$(cat tmp_ui_gate_serve.pid)" 2> /dev/null; exit 1; \
	fi
	@status=0; PB_UI_EXCLUDE_LIVE=1 PB_UI_BASE_URL=$(UI_GATE_URL) sh -c 'cd web && npm run test:ui' || status=1; \
	kill "$$(cat tmp_ui_gate_serve.pid)" 2> /dev/null; rm -f tmp_ui_gate_serve.pid $(UI_GATE_CFG); \
	exit $$status

# Живой корпус (не гейт): весь сьют, включая ui_live, против operator-serve.
ui-live:
	cd web && npm run test:ui
