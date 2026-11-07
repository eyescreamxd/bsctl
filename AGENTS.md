# AGENTS.md — заметки для агентов

Этот файл для ИИ-агентов, которые продолжают работу над `bsctl` (Claude Code, Codex и т. п.). `CLAUDE.md` его импортирует.
Пользовательская документация — `README.md` (для GitHub) и `README.txt` (лежит рядом с exe).

## Что это

`bsctl` — замена Razer Synapse на Go для одного ПК под Windows 11. Устройства:

- гарнитура Razer BlackShark V2 Pro 2023, донгл `1532:0555`;
- мышь Basilisk V3 Pro;
- зарядная станция Mouse Dock Pro.

Одна кодовая база собирается в два exe:

- `bsctl.exe` — консольные команды и диагностика;
- `BlackSharkTray.exe` — трей. Это та же программа с `-H=windowsgui -X main.defaultMode=tray`.

Трей делает следующее:

- опрашивает устройства и показывает заряд двумя значками;
- шлёт гарнитуре настройки из `bsctl.json`;
- красит док по заряду мыши;
- ведёт шумодав через Equalizer APO;
- поднимает окно настроек: локальный HTTP плюс Edge `--app`.

## Правила

- Язык. Интерфейс, логи, сообщения об ошибках и пользовательские доки пишутся по-русски. Идентификаторы и комментарии в коде — по-английски.
- Зависимости: только стандартная библиотека и `golang.org/x/sys`. Без CGO.
- Платформы. Код под Windows лежит в `*_windows.go`. Логику без Win32 (протоколы, разбор, генерация конфигов, HTTP окна) держи в файлах
  без суффикса, тогда `go test ./...` проходит на Linux.
- Раскладка Go: `cmd/bsctl` (`package main`, там же `var defaultMode` для `-X main.defaultMode=tray`) и пакеты в `internal/`.
  Экспортируй только то, что нужно другим пакетам. `config` импортирует `headset` и `eapo`, поэтому `headset` и `eapo`
  не импортируют `config`: `Apply` получает `headset.Settings`, `Open` — `headset.Conn`, папку exe дают `internal/paths`.
- После правок прогоняй `scripts\check.cmd` (на Windows) или вручную:
  ```
  gofmt -l cmd internal           # пусто
  go vet ./... && GOOS=linux go vet ./... && GOOS=windows GOARCH=amd64 go vet ./...
  go test ./...
  go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...   # и с GOOS=linux / GOOS=windows
  ```
  На Windows `GOOS=linux go test` не запустится (linux-бинарники), поэтому `check.cmd` для Linux делает `vet` и `build`,
  а `staticcheck` ставит в `%TEMP%\bsctl-tools`.
- Версия задаётся в `internal/version/version.go`; поднимай её при каждой выдаче пользователю. Первая строка `README.txt` тоже содержит номер версии.
- Форматы файлов закреплены в `.gitattributes`:
  - `.cmd` — только ASCII и CRLF: cmd.exe путается в UTF-8;
  - `.txt` — UTF-8 и CRLF;
  - `.go`, `.md`, `.html` — LF.
- `<title>` в `internal/ui/web/index.html` должен совпадать с `ui.SettingsTitle`: трей ищет окно Edge по заголовку. Это проверяет тест `TestSettingsTitle`.
- Не коммитить: `dist/`, `*.exe`, `bsctl.json`, `bsctl.log*`. Плагины в `third_party/rnnoise/` и `third_party/dfn/` коммитятся как есть,
  неизменёнными, с лицензиями и `README.txt`.
- Лицензия проекта — WTFPL v2 (`LICENSE`).
- Внешние пути не хардкодить. Equalizer APO берётся из `HKLM\SOFTWARE\EqualizerAPO` (`ConfigPath`), Edge — из App Paths.

## Карта кода

| Файл | Что |
|---|---|
| `cmd/bsctl/main_windows.go` | разбор команд, `usage`, диагностика (`probe`, `monitor`, `mouse`, `eapo`); `main_other.go` — заглушка не под Windows |
| `internal/headset/proto.go` | протокол гарнитуры: сборка и разбор пакетов, пресеты EQ, `Settings`, `Conn` |
| `internal/headset/headset_windows.go` | донгл: поток чтения, `tx` с ожиданием ответа, `Open`, `Apply`, sidetone |
| `internal/razer/razer.go`, `mouse_windows.go` | Razer 2.5 (мышь и док): пакеты, crc, заряд, эффекты, `Link`, события дока |
| `internal/hid/hid_windows.go` | HID: перечисление (CfgMgr32), чтение и запись, feature-отчёты, CancelIoEx, `ProcessRunning`; `hex.go` — `Hex`, `TrimZeros` |
| `internal/wintray/wintray_windows.go` | свой трей на Win32: несколько значков, меню, TaskbarCreated |
| `internal/tray/tray_windows.go` | `trayApp`: воркеры гарнитуры (`hsWorker`) и мыши (`msWorker`), меню, `bsctl.json` |
| `internal/icon/icon.go` | значки рисуются кодом и пакуются в ICO |
| `internal/ui/ui.go`, `web/index.html` | HTTP окна настроек: токен, проверка Host, `/api/state`, `/api/do`, страница |
| `internal/tray/ui_windows.go` | бэкенд окна: `State()`/`Do()`, запуск Edge, `setNoise` для меню |
| `internal/tray/settingswin_windows.go` | поиск окна Edge по заголовку, восстановление места, запись перемещений |
| `internal/eapo/eapo.go` | генерация `bsctl.txt`/`config.txt` (шумодав, `GraphicEQ` микрофона, усиление), движки шумодава (`Engines`), разбор WAVEFORMATEX |
| `internal/eapo/eapo_windows.go` | поиск APO и микрофона в реестре, проверки, `Apply`, копирование плагинов |
| `internal/eapo/audiofmt_windows.go` | формат микрофона через MMDevice API (COM, `PKEY_AudioEngine_DeviceFormat`) |
| `internal/audio/audio_windows.go` | waveIn: держит микрофон открытым для самопрослушивания |
| `internal/config/config.go` | `bsctl.json`: умолчания, проверка, сохранение через `.tmp` и rename; `bsctl.log` |
| `internal/autostart/` | ключ `HKCU\…\Run`; при старте трей переписывает путь, если exe переехал |
| `internal/paths/`, `internal/version/` | папка exe, папка состояния (`StateDir`, портативный режим); версия |
| `internal/update/` | последний релиз на GitHub, сравнение версий, скачивание установщика с проверкой SHA-256 |
| `internal/tray/update_windows.go` | проверка обновлений раз в 6 часов, пункт меню, запуск установщика `/SILENT` и выход |
| `installer/bsctl.iss` | Inno Setup (UTF-8 с BOM, CRLF): установка в `%LOCALAPPDATA%\Programs\bsctl` без UAC |
| `.github/workflows/release.yml` | релиз по тегу `vX.Y.Z`; ручной запуск — только артефакт |
| `scripts/` | `build.cmd` (сборка в `dist\`), `check.cmd`, `update.cmd`, диагностика `1-probe.cmd` … `5-dock-test.cmd` |

## Выдача пользователю

- Два режима. **Установленный** (Inno Setup, `%LOCALAPPDATA%\Programs\bsctl`): состояние в `%LOCALAPPDATA%\bsctl\`,
  трей сам проверяет релизы и ставит обновление по пункту меню. **Портативный** (рядом с exe лежит `bsctl.json`,
  так собирается `dist\`): состояние рядом с exe, обновления не проверяются. Режим выбирает `paths.Portable()`.
- Релиз: подними версию в `internal/version/version.go` и первой строке `README.txt`, закоммить, `git tag vX.Y.Z`,
  `git push origin vX.Y.Z`. Workflow сверит тег с версией, прогонит проверки, соберёт установщик и создаст Release
  с `bsctl-setup-X.Y.Z.exe` и `.sha256` (имена важны: их ищет `update.Release.Installer`). Репозиторий публичный:
  трей читает `api.github.com` без токена.
- Деинсталлятор вызывает `bsctl uninstall-cleanup`: автозапуск выкл, из APO убираются `Include: bsctl.txt`, `bsctl.txt`
  и `config\bsctl\`. Установщик перед копированием закрывает все `BlackSharkTray.exe` (и портативный тоже).
- Разработка: `scripts\build.cmd` собирает в `dist\` (портативно) и копирует туда плагины, скрипты и `README.txt`;
  запуск — `dist\update.cmd`. Запущенный `BlackSharkTray.exe` занят Windows, поэтому новый трей всегда собирается как
  `BlackSharkTray.new.exe`, а `update.cmd` его подменяет. `bsctl.exe` можно перезаписывать.
- После обновления посмотри `bsctl.log` (в папке состояния): там должны быть строки `tray X started` и
  `Equalizer APO: <микрофон>, 48000 Гц, 1 кан., 16 бит [Windows Audio] …`. Если автозапуск смотрел на старый путь,
  там же будет `автозапуск: путь обновлён на …`.
- `bsctl.json` и `bsctl.log` — состояние пользователя, их не перезаписывать.
- Сборка из Linux (плагины, скрипты и `README.txt` тогда скопируй в `dist/` сам):
  ```
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o dist/bsctl.exe ./cmd/bsctl
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H=windowsgui -X main.defaultMode=tray" -o dist/BlackSharkTray.new.exe ./cmd/bsctl
  ```
- GitHub: публичный репозиторий `github.com/eyescreamxd/bsctl`, дальше обычный `git`.

## Как проверять без железа

- **Окно настроек.** Команда `UI_PREVIEW_SECONDS=30 go test -v -run 'TestUIServer$' ./internal/ui` печатает адрес фейкового бэкенда.
  Скриншоты снимай Playwright (Chromium): светлая и тёмная темы, ширины 944 и 420.
- **Значки.** `PREVIEW_DIR=<папка> go test -run TestIconPreview ./internal/icon` рисует лист со всеми состояниями.
- **Wine + Xvfb.**
  - `bsctl.exe tray-selftest` проверяет значки и меню.
  - `bsctl.exe eapo` и `ns ...` проверяют APO. Для этого подделай реестр через `wine reg add`:
    - `HKLM\SOFTWARE\EqualizerAPO` с `ConfigPath`;
    - CLSID, у которого `InprocServer32` содержит `EqualizerAPO`;
    - этот CLSID в `FxProperties` эндпоинта записи, ключ `{d04e05a6-…},5`;
    - имя эндпоинта `{b3f8fa53-0004-438e-9003-51a46e139bfc},6` = `Razer BlackShark V2 Pro`.
  - Под Wine работает и COM `MMDevice`: формат возвращается как 48000 Гц, 2 канала, 32 бит.
- **Плагины.** Минимальный VST2-хост на Go, повторяющий Equalizer APO, запускается под Wine. Порядок вызовов:
  1. `VSTPluginMain`;
  2. `effOpen`;
  3. `setSampleRate` 48000;
  4. `setBlockSize` 480;
  5. `mainsChanged(1)`;
  6. `startProcess`;
  7. `processReplacing`.

  Лишние входы плагина хост, как и APO, кормит тишиной. Так меряли подавление, уровень, задержку и нагрузку.
- **Особенности Wine:**
  - в `cmd` операторы `&&`/`||` после перенаправлений работают неправильно, поэтому в `.cmd` используй `if errorlevel`;
  - `where` в Wine — заглушка;
  - mmdevapi в Wine хранит в реестре голый WAVEFORMATEX и перезаписывает его при каждом создании энумератора;
  - `DestroyWindow` в самодельном окне-заглушке может зависнуть, поэтому в тестах проверяй факт получения `WM_CLOSE`.

## Факты о железе (проверено на устройстве)

### Гарнитура

Подробности в `internal/headset/proto.go`.

- Пакеты: HID output report 2, 64 байта: `02 80 <len> 00 00 'P' 'A' <target> 00 <blockLen> <cmd> <lenHi> <lenLo> <data>`.
  `target` — `0x08` для гарнитуры, `0x0E` для самого донгла.
- Пары команд get/set:

  | Get / Set | Что |
  |---|---|
  | 0x13 / 0x93 | индекс пресета |
  | 0x15 / 0x95 | EQ |
  | 0x16 / 0x96 | пресет EQ микрофона |
  | 0x17 / 0x97 | свой EQ микрофона (звук не меняет) |
  | 0x18 / 0x98 | самопрослушивание |
  | 0x19 / 0x99 | громкость самопрослушивания, 0..15 = `floor(p·15/100)` |
  | 0x1D / 0x9D | группа пресетов |
  | 0x1E / 0x9E | флаг EQ динамиков |
  | 0x2C / 0xAC | автовыключение |
  | 0x55 / 0xE9 | микрофон вкл/выкл |
  | 0x20, 0x21, 0x2A | связь, заряд, зарядка (только чтение) |
  | 0xE1 | режим донгла: 1 — ПК управляет |

  Уведомления приходят с тем же кодом и `op1 = 2`.
- Каждый опрос делает keep-alive, утверждая remote mode. Сброс настроек донглом виден по флагу 0x1E: трей сравнивает его со значением после
  `Apply` и отправляет настройки заново. После трёх сбросов за 10 минут трей перестаёт следить за флагом.
  В v0.3.0 была ошибка: настройки переотправлялись каждые 5 секунд.
- Самопрослушивание звучит, только если отправлена громкость 0x99 и микрофон кем-то записывается. Synapse держит запись через THX.
  У нас это делает `audio_windows.go`: waveIn на 48 кГц, моно.
- EQ микрофона в гарнитуре не работает. Проверено 2026-11-07: полосы −12 (0x97) и пресет «Конференция» (0x96) прошивка подтверждает,
  но ни собеседник, ни самопрослушивание разницы не слышат. Synapse 0x97 этой гарнитуре не шлёт. Поэтому `Apply` шлёт пресет 0,
  а EQ микрофона делает Equalizer APO (`GraphicEQ` в `bsctl.txt`, `Config.MicEQ`).
- Самопрослушивание идёт внутри гарнитуры, мимо Windows: шумодав и EQ из APO в нём не слышны.

### Мышь и док

Razer 2.5:

- feature-отчёт: 90 байт плюс report ID 0;
- crc — XOR байтов 2..87;
- transaction id: `0xE0|n` для дока, `n` для мыши через док;
- коллекция дока: `MI_00`, usage `0001:0002`.

Команды:

| Команда | Что |
|---|---|
| `07/80` | заряд |
| `07/84` | зарядка |
| `0F/02` | эффект: 1 static, 2 breathing, 3 spectrum |
| `0F/04` | яркость |
| `00/84`, `00/04` | режим устройства (чтение / запись) |

События дока приходят входным отчётом 5: `05 31 …` — заряд, `05 09 …` — состояние.

Цвета как в Synapse (`DeviceManifest_164`):

- от 75% — зелёный;
- 50–74% — жёлтый `FFFF00`;
- 25–49% — оранжевый `FF4500`;
- меньше 25% — красный;
- при зарядке — дыхание, кроме 100%.

### Equalizer APO

- Конфиги лежат в `C:\Program Files\EqualizerAPO\config`, пользователь может туда писать.
- Трей пишет только `bsctl.txt`, `user.txt` и строку `Include: bsctl.txt` в `config.txt`.
  Стоковый `config.txt` с демо-фильтрами он заменяет, оставив копию `.bsctl-backup`.
  По кнопке «Отключить демо-фильтр» трей комментирует `Include: example.txt`.
- В `bsctl.txt` ставится `Device: {GUID эндпоинта}` и `Stage: capture`. В конце состояние сбрасывается в умолчание:
  `Stage: post-mix capture` и `Device: all`.
- Equalizer APO умеет только VST2. Плагины лежат в `config\bsctl\`: `audiodg` работает от LocalService и не читает профиль пользователя.
- Каналы. Если у плагина больше входов, чем у устройства, лишние получают тишину. Alt Denoiser — стерео и усредняет входы,
  поэтому на моно-микрофоне перед ним нужен `Preamp: 6.02 dB`. RNNoise (`rnnoise_mono`) берёт первый канал и поправки не требует.
- RNNoise работает только при формате микрофона 48 кГц. Alt Denoiser ресемплит сам, но ему нужен VC++ 2015–2022 runtime.
  Задержки: RNNoise ~20 мс, DeepFilterNet ~70 мс (1920 сэмплов FIFO плюс модель).
- Формат микрофона в реестре (`{f19f064d-…},0`) хранится с 8-байтным заголовком: `41 00 00 00 01 00 00 00` + WAVEFORMATEX.
  Наивное чтение со смещением 4 давало «1 Гц». Правильный путь — COM (`audiofmt_windows.go`), запасной — поиск структуры (`parseWaveFormat`).
- Что APO стоит на микрофоне, определяется так: один из `FxProperties\{d04e05a6-594b-4fb6-a80d-01af5eed7d1d},N` указывает на CLSID
  с `EqualizerAPO.dll` в `InprocServer32`. При смене драйвера, например при удалении THX, Configurator надо запускать заново.
- После установки APO на устройства вывода у пользователя пропадал звук везде: APO конфликтовал с THX от Synapse.
  Поэтому APO ставится только на микрофон.

### Окно настроек

- Сервер слушает `127.0.0.1:0`. Токен передаётся в `?t=` или в заголовке `X-Token`; `Host` проверяется против DNS-rebinding; заданы CSP и `X-Frame-Options`.
- Окно Edge `--app` — чужое окно (класс `Chrome_WidgetWin_1`). Трей ищет его по префиксу заголовка и восстанавливает `settingsWindow` в течение 2 секунд.
  Дальше раз в 0,5 с трей сверяет прямоугольник окна и сохраняет его, когда оно перестаёт двигаться. Поток работает в контексте DPI per-monitor v2.
  При новом запуске трей закрывает окна, оставшиеся от прошлого процесса; при выходе закрывает своё.

## Открытые задачи и идеи

- Удалить Razer Synapse, THX Spatial Audio и задачу `BlackShark-Resume-Synapse`, затем перенастроить APO на микрофоне.
- По желанию собрать Alt Denoiser с пост-фильтром DeepFilterNet (`df_set_post_filter_beta`) и выводом порогов.
  У апстрима есть сборка в GitHub Actions.
- ~~Выбрать лицензию проекта.~~ Сделано: WTFPL v2.
