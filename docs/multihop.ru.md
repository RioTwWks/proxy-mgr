**[English](multihop.md)** | Русский

# Мульти-хоп цепочки (RU entry → EU exit)

RioNexGate поддерживает цепочки прокси **entry → exit**: клиенты подключаются только к **входному** узлу (например, сервер в России), а в Интернет трафик выходит с **выходного** узла (например, сервер в ЕС). Панель на entry-сервере генерирует Xray outbound, которые ретранслируют трафик пользователей на зарегистрированные exit-узлы.

Это полное руководство по проектированию, развёртыванию, эксплуатации и устранению неполадок мульти-хоп топологий. Основано на реализации в `backend/internal/core/multihop.go`, `backend/internal/core/templates/xray.json.tmpl`, `backend/internal/models/node.go` и UI страниц Nodes / User chain.

> **Область применения:** генерация outbound и routing для цепочек реализована для **Xray-core** (`core.type: xray`). Шаблон sing-box **не** создаёт chain outbound — для мульти-хопа используйте Xray на entry-сервере. См. [Ограничения](#ограничения).

---

## Содержание

1. [Глоссарий](#глоссарий)
2. [Архитектура](#архитектура)
3. [Сеть и матрица файрвола](#сеть-и-матрица-файрвола)
4. [Топология развёртывания](#топология-развёртывания)
5. [Полное пошаговое развёртывание](#полное-пошаговое-развёртывание)
6. [Справочник credentials JSON](#справочник-credentials-json)
7. [Анатомия Xray-конфига](#анатомия-xray-конфига)
8. [Руководство по UI панели (`/nodes`)](#руководство-по-ui-панели-nodes)
9. [Назначение цепочки пользователю](#назначение-цепочки-пользователю)
10. [Подписка и поведение клиента](#подписка-и-поведение-клиента)
11. [Stealth и мульти-хоп вместе](#stealth-и-мульти-хоп-вместе)
12. [Примеры конфигурации](#примеры-конфигурации)
13. [Справочник API](#справочник-api)
14. [Процедуры проверки](#процедуры-проверки)
15. [Устранение неполадок](#устранение-неполадок)
16. [Заметки по безопасности](#заметки-по-безопасности)
17. [Продакшен-чеклист](#продакшен-чеклист)
18. [FAQ](#faq)
19. [Ограничения](#ограничения)
20. [См. также](#см-также)

---

## Глоссарий

| Термин | Определение |
|--------|-------------|
| **Entry-узел (входящий)** | Клиентский hop. Пользователи подключаются сюда по VLESS (опционально Reality / XHTTP / Vision). Хранится в таблице `nodes` с `role: entry`. Ссылки, QR-коды и подписки всегда указывают на `address:port` entry-узла. |
| **Exit-узел (исходящий)** | Hop выхода в Интернет. Xray на entry-сервере открывает VLESS outbound на этот хост, используя relay credentials из записи exit-узла. Хранится с `role: exit`. Клиентам не публикуется. |
| **Relay user (релейный пользователь)** | Выделенный VLESS-пользователь на EU exit inbound; его UUID копируется в `credentials.uuid` exit-узла на RU-панели. Не совпадает с UUID конечных пользователей. |
| **Chain tag (тег цепочки)** | Тег Xray outbound для chained freedom outbound: `exit-<name>-chain`, где `<name>` — уникальное поле `name` exit-узла. Правила routing направляют трафик пользователей на этот тег. |
| **Outbound tag (тег исходящего)** | Прямой VLESS outbound на exit: `exit-<name>` (из `Node.OutboundTag()`). Outbound `-chain` проксирует через этот тег через `proxySettings`. |
| **`local_role`** | Поле в `core.multihop` (`entry` или другое). Только `enabled: true` **и** `local_role: entry` запускает `BuildMultihopData` и генерацию цепочки (`MultihopConfig.IsEntryNode()`). |
| **Авторазрешение** | Если у пользователя нет явных `entry_node_id` / `exit_node_id`, панель выбирает узел с **наименьшим `priority`** среди **активных** узлов этой роли (`ORDER BY priority ASC, id ASC`). |
| **`ResolveClientEndpoint`** | Go-функция, возвращающая `{Host, Port}` для клиентского вывода. Использует разрешённый entry-узел, если есть; иначе — `core.public_host` и `core.listen_port`. Exit-узлы никогда не передаются. |

---

## Архитектура

### Общая схема

```mermaid
flowchart LR
  C[Client] -->|VLESS + Stealth inbound| RU[RU entry server\nRioNexGate + Xray]
  RU -->|VLESS outbound\nrelay credentials| EU[EU exit server\nXray inbound]
  EU -->|freedom| NET[Internet]
```

### Последовательность подключения

```mermaid
sequenceDiagram
  participant Client
  participant RU as RU entry Xray
  participant EU as EU exit Xray
  participant Web as Target website

  Client->>RU: VLESS handshake (Reality/XHTTP/Vision)
  Note over Client,RU: DPI видит только клиент ↔ RU
  RU->>RU: Routing rule matches user email
  RU->>EU: VLESS outbound (relay UUID + hop transport)
  EU->>Web: TCP/TLS to destination
  Web-->>EU: Response
  EU-->>RU: Relayed traffic
  RU-->>Client: Response
  Note over Web: Видит EU egress IP
```

ASCII-эквивалент:

```
  Client
    |
    |  VLESS (+ Reality / XHTTP / Vision per core.stealth)
    v
  +---------------------------+
  | RU entry server           |
  | RioNexGate panel + Xray   |
  | - user inbounds           |
  | - multihop outbounds      |
  +---------------------------+
    |
    |  VLESS to exit node (stored credentials)
    v
  +---------------------------+
  | EU exit server            |
  | Xray inbound (relay user) |
  +---------------------------+
    |
    v
  Internet (EU egress IP)
```

**Что видит клиент:** ссылки подписки, QR-коды и конфиги RioNexTunnel указывают только на **entry** host/port (`ResolveClientEndpoint`). Exit-узлы клиентам не публикуются.

**Что содержит Xray-конфиг на entry:** для каждого активного exit-узла, используемого хотя бы одним активным пользователем, генератор добавляет:

1. **VLESS outbound** на `address:port` exit с credentials из записи узла (тег: `exit-<name>`).
2. **freedom outbound** с `proxySettings`, цепляющимся к VLESS outbound (тег: `exit-<name>-chain`).
3. **Правило routing**, сопоставляющее email пользователей с тегом chained outbound (`exit-<name>-chain`).

См. [`backend/internal/core/templates/xray.json.tmpl`](../backend/internal/core/templates/xray.json.tmpl) и [`multihop.go`](../backend/internal/core/multihop.go).

---

## Сеть и матрица файрвола

### Справочник портов

| Порт | Протокол | Направление | Сервис | Примечания |
|------|----------|-----------|---------|-------|
| **443** | TCP | Клиент → RU | XHTTP + Reality inbound (типично) | Основной stealth-порт при `core.stealth.xhttp.enabled: true` |
| **8443** | TCP | Клиент → RU | Vision + Reality inbound (типично) | Дополнительный stealth-порт при `core.stealth.vision.enabled: true` |
| **2053** | TCP | Клиент → RU | VLESS + TLS inbound (опционально) | При `core.stealth.tls.enabled: true`; поддерживает фрагментацию |
| **8888** | TCP | Admin → RU | RioNexGate panel (nginx) | Меняется через `HTTP_PORT` в `.env`; в продакшене HTTPS |
| **8080** | TCP | Локально / отладка | Backend API напрямую | Не нужен снаружи при проксировании nginx `/api` |
| **8443** (пример) | TCP | RU → EU | Exit relay inbound | Совпадает с `port` exit-узла и EU inbound; Vision типичен |
| **443** (альтернатива) | TCP | RU → EU | Exit relay inbound | Если EU слушает 443 вместо 8443 |
| **10085** | TCP | Docker internal | Xray stats API | `core.xray.api_address`; не для клиентов |

### Минимальные правила файрвола

| Правило | Источник | Назначение | Порт | Действие |
|------|--------|-------------|------|--------|
| Доступ клиентов | Интернет / сети пользователей | Публичный IP RU | 443, 8443 (+ TLS-порт при использовании) | ALLOW |
| Межузловой релей | Приватный/публичный IP RU | Публичный IP EU | Порт exit-узла (напр. 8443) | ALLOW |
| Админ панели | IP администратора | Публичный IP RU | 8888 или HTTPS | ALLOW (ограничить источник) |
| EU-панель (опционально) | IP администратора | Публичный IP EU | 8888 | ALLOW только если RioNexGate на EU |
| Блокировка клиентов на EU | Интернет | Публичный IP EU | 443, 8443 | DENY если EU только релей (рекомендуется) |

### Проверка связности

С **RU entry-сервера**:

```bash
# TCP to EU relay port (replace host/port)
nc -zv eu.example.com 8443

# DNS resolution
dig +short eu.example.com

# Latency (health check uses 5s dial timeout)
time nc -zv eu.example.com 8443
```

С **рабочей станции клиента**:

```bash
# Entry stealth ports
nc -zv ru.example.com 443
nc -zv ru.example.com 8443
```

---

## Топология развёртывания

| Сервер | Роль | Панель RioNexGate | `core.multihop` | Назначение |
|--------|------|------------------|-----------------|---------|
| RU | Entry | **Да** (основная) | `enabled: true`, `local_role: entry` | Inbound для клиентов, управление цепочками, outbound в EU |
| EU | Exit | Опционально (локальный админ) | `enabled: false` (или не указывать) | Inbound-реле для entry; выход в Интернет |

**Панель управления находится на entry-сервере.** Exit-серверам достаточно соответствующего Xray inbound для relay credentials, которые вы сохраняете в записи exit-узла. Запуск RioNexGate на EU удобен для управления пользователями и credentials, но для самой цепочки не обязателен.

### Когда регистрировать entry-узел

Запись entry-узла **опциональна**, но рекомендуется когда:

- `core.public_host` отличается от hostname, который должны видеть клиенты (CDN, anycast, несколько RU IP).
- У вас несколько entry-серверов и нужно назначать entry на пользователя.
- Нужна диаграмма топологии `/nodes` с реальным клиентским endpoint.

Без entry-узла `ResolveClientEndpoint` использует `core.public_host` + `core.listen_port`.

---

## Полное пошаговое развёртывание

Этот раздел описывает полное развёртывание RU entry → EU exit от пустых серверов до проверенного egress в EU.

### Фаза A — RU entry-сервер

#### A.1 Подготовка ОС

1. Создайте Linux VPS (рекомендуется Ubuntu 22.04/24.04 или Debian 12) в России (или в вашем entry-регионе).
2. Задайте hostname и часовой пояс:

```bash
sudo hostnamectl set-hostname ru-entry
sudo timedatectl set-timezone Europe/Moscow
```

3. Обновите пакеты и установите зависимости:

```bash
sudo apt update && sudo apt upgrade -y
sudo apt install -y git make curl jq ufw
```

4. Настройте UFW (укажите IP админа):

```bash
sudo ufw allow 22/tcp
sudo ufw allow 443/tcp
sudo ufw allow 8443/tcp
sudo ufw allow 8888/tcp    # restrict to admin IP in production
sudo ufw enable
```

#### A.2 Установка Docker

```bash
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker "$USER"
# Перелогиньтесь для применения группы docker
docker compose version
```

#### A.3 Клонирование RioNexGate и инициализация

```bash
git clone https://github.com/RioTwWks/RioNexGate.git
cd RioNexGate
make init
```

`make init` создаёт `backend/config.yaml`, `.env` и каталоги `data/`.

#### A.4 Генерация пары Reality (RU — для клиентов)

На RU-хосте или любой машине с Xray:

```bash
docker run --rm ghcr.io/xtls/xray-core:latest x25519
```

Пример вывода:

```
Private key: aB3dEf9GhIjKlMnOpQrStUvWxYz0123456789AbCdEfGh
Public key:  XyZ9wVuTsRqPoNmLkJiHgFeDcBa9876543210ZyXwVuTs
```

Скопируйте **Private key** → `core.stealth.reality.private_key`  
Скопируйте **Public key** → `core.stealth.reality.public_key` (нужен клиентам в ссылках; не секрет)

#### A.5 Настройка RU `backend/config.yaml` (построчно)

Отредактируйте `backend/config.yaml`. Ниже — аннотированный продакшен-конфиг entry. Добавьте блок `multihop` — в `config.example.yaml` его нет по умолчанию, но для цепочек он обязателен.

```yaml
server:
  port: 8080
  api_key: "REPLACE_WITH_LONG_RANDOM_KEY"   # panel login; rotate from default

database:
  path: "./data/rionexgate.db"

core:
  type: xray                              # REQUIRED for multi-hop chain generation
  listen_port: 443                        # fallback port when no entry node port set
  public_host: "ru.example.com"           # fallback host in client links
  stats_poll_seconds: 60

  multihop:
    enabled: true                         # turn on BuildMultihopData
    local_role: entry                     # only "entry" generates chain outbounds

  xray:
    config_path: "./data/xray/config.json"
    binary_path: "/usr/local/bin/xray"
    api_address: "host.docker.internal:10085"

  stealth:
    enabled: true
    fingerprint: firefox                  # firefox or edge; avoid chrome/safari per stealth docs
    reality:
      dest: "www.microsoft.com:443"       # believable TLS front target
      server_names:
        - "www.microsoft.com"
      private_key: "RU_REALITY_PRIVATE_KEY_FROM_x25519"
      public_key: "RU_REALITY_PUBLIC_KEY_FROM_x25519"
      short_ids:
        - "a1b2c3d4"
      show: false
      xver: 0
    xhttp:
      enabled: true
      port: 443                           # client primary port
      path: "/api/v1/data"
      mode: stream-one                    # do not use auto — known client bugs
      tag: vless-xhttp-reality
    vision:
      enabled: true
      port: 8443                          # client secondary port
      tag: vless-vision-reality
    tls:
      enabled: false                      # optional; enable only if you need TLS+fragmentation inbound
    fragmentation:
      enabled: false
    awg:
      enabled: false                      # incompatible with multi-hop chains; see Limitations

telegram:
  bot_token: ""
  admin_ids: []

limits:
  default_traffic_gb: 50
  default_expire_days: 30
```

Значения полей:

| Поле | Назначение |
|-------|---------|
| `core.type: xray` | Только шаблон Xray генерирует multihop outbound |
| `core.multihop.enabled` | Главный переключатель генерации цепочки |
| `core.multihop.local_role: entry` | Этот хост — оркестратор цепочки |
| `core.public_host` | Hostname entry по умолчанию, если entry-узел не разрешён |
| `core.stealth.xhttp.port` | Порт для XHTTP-профиля клиентов (обычно 443) |
| `core.stealth.vision.port` | Порт для Vision-профиля клиентов (обычно 8443) |
| `core.stealth.reality.*` | Клиентские параметры Reality (отдельно от ключей EU hop) |

#### A.6 Запуск RioNexGate и Xray

```bash
make up          # detached: backend, frontend, nginx
make dev-cores   # xray-core container (profile cores)
```

Откройте панель: `http://ru.example.com:8888` → введите API key из `server.api_key`.

#### A.7 Проверка RU-стека перед добавлением EU

```bash
curl -s -H "X-API-Key: YOUR_KEY" http://localhost:8888/api/health | jq .
docker compose exec xray-core xray run -test -c /etc/xray/config.json
```

---

### Фаза B — EU exit-сервер

Доступны два варианта. Оба требуют VLESS inbound, принимающий relay UUID с RU.

#### Вариант B1 — Полный RioNexGate на EU (рекомендуется)

**B1.1** Повторите A.1–A.3 на EU VPS.

**B1.2** Сгенерируйте **отдельную** пару Reality-ключей для EU hop (не переиспользуйте RU-ключи):

```bash
docker run --rm ghcr.io/xtls/xray-core:latest x25519
```

**B1.3** Настройте EU `backend/config.yaml`:

```yaml
server:
  port: 8080
  api_key: "EU_ADMIN_KEY_DIFFERENT_FROM_RU"

database:
  path: "./data/rionexgate.db"

core:
  type: xray
  listen_port: 443
  public_host: "eu.example.com"
  stats_poll_seconds: 60

  multihop:
    enabled: false                        # exit host does not generate chains

  xray:
    config_path: "./data/xray/config.json"
    binary_path: "/usr/local/bin/xray"
    api_address: "host.docker.internal:10085"

  stealth:
    enabled: true
    fingerprint: firefox
    reality:
      dest: "www.cloudflare.com:443"
      server_names:
        - "www.cloudflare.com"
      private_key: "EU_REALITY_PRIVATE_KEY"
      public_key: "EU_REALITY_PUBLIC_KEY"
      short_ids:
        - "b2c3d4e5"
      show: false
      xver: 0
    xhttp:
      enabled: false                      # hop typically uses Vision on 8443
    vision:
      enabled: true
      port: 8443                          # RU will connect here
      tag: vless-vision-reality
    tls:
      enabled: false
    awg:
      enabled: false

limits:
  default_traffic_gb: 9999
  default_expire_days: 3650
```

**B1.4** Запустите сервисы:

```bash
make up
make dev-cores
```

**B1.5** Создайте relay user в EU-панели:

1. Откройте `http://eu.example.com:8888` (ограничьте файрволом).
2. **Users** → **Add user**.
3. Email: `relay@eu.internal` (любой уникальный email).
4. Скопируйте **UUID** пользователя со страницы деталей — он станет `credentials.uuid` на RU.

**B1.6** Запишите параметры EU hop для exit-узла на RU:

| Поле exit-узла на RU | Источник на EU |
|--------------------|-----------|
| `address` | `eu.example.com` (должен быть доступен с RU) |
| `port` | `8443` (`core.stealth.vision.port`) |
| `credentials.uuid` | Relay user UUID |
| `credentials.public_key` | `core.stealth.reality.public_key` |
| `credentials.short_id` | First entry in `core.stealth.reality.short_ids` |
| `credentials.flow` | `xtls-rprx-vision` |
| `credentials.security` | `reality` |
| `credentials.network` | `tcp` |

#### Вариант B2 — Standalone Xray на EU (минимальный)

Используйте, если панель на EU не нужна. Создайте `/etc/xray/config.json`:

```json
{
  "log": {
    "loglevel": "warning"
  },
  "inbounds": [
    {
      "tag": "relay-vision",
      "listen": "0.0.0.0",
      "port": 8443,
      "protocol": "vless",
      "settings": {
        "clients": [
          {
            "id": "550e8400-e29b-41d4-a716-446655440000",
            "email": "relay@eu.internal",
            "flow": "xtls-rprx-vision",
            "level": 0
          }
        ],
        "decryption": "none"
      },
      "streamSettings": {
        "network": "tcp",
        "security": "reality",
        "realitySettings": {
          "show": false,
          "dest": "www.cloudflare.com:443",
          "xver": 0,
          "serverNames": ["www.cloudflare.com"],
          "privateKey": "EU_REALITY_PRIVATE_KEY",
          "shortIds": ["b2c3d4e5"]
        }
      },
      "sniffing": {
        "enabled": true,
        "destOverride": ["http", "tls", "quic"]
      }
    }
  ],
  "outbounds": [
    {
      "protocol": "freedom",
      "tag": "direct"
    }
  ]
}
```

Сгенерируйте ключи через `xray x25519`, замените `privateKey` и используйте соответствующий **публичный** ключ и `shortIds` в credentials exit-узла на RU.

Запуск и проверка:

```bash
xray run -test -c /etc/xray/config.json
xray run -c /etc/xray/config.json
sudo ufw allow 8443/tcp
```

---

### Фаза C — Регистрация узлов на RU-панели

Перейдите на **`/nodes`** в RU-панели (боковое меню: **Nodes** / **Multi-hop nodes**).

#### C.1 Создание entry-узла (рекомендуется)

1. Нажмите **Add node**.
2. Заполните форму:
   - **Name:** `entry-ru` (slug; только для UI entry-узлов)
   - **Role:** `Entry`
   - **Address:** `ru.example.com` (куда подключаются клиенты)
   - **Port:** `443` (основной XHTTP-порт; или `8443`, если только Vision)
   - **Region:** `RU`
   - **Priority:** `10` (меньше = предпочтительнее при авто-выборе)
   - **Active:** отмечено
3. Нажмите **Create**.

Диаграмма топологии обновится и покажет `entry-ru` в блоке Entry.

#### C.2 Создание exit-узла (обязательно)

1. Нажмите **Add node**.
2. Заполните форму:
   - **Name:** `exit-eu` (станет outbound-тегом `exit-exit-eu`)
   - **Role:** `Exit`
   - **Address:** `eu.example.com`
   - **Port:** `8443`
   - **Region:** `EU`
   - **Priority:** `10`
   - **Protocol:** `vless`
   - **Active:** отмечено
3. Разверните **Credentials** и введите UUID, Public key, Short ID (или вставьте полный JSON через API — см. ниже).
4. Нажмите **Create**.

Для полных credentials (flow, network, path) используйте API — форма UI показывает только UUID, public key и short ID; дополнительные поля — через `POST /api/nodes` или `PUT /api/nodes/{id}`.

#### C.3 Проверка health-check

1. В таблице **Exit nodes** нажмите **Health check** у `exit-eu`.
2. Ожидайте зелёный **TCP OK (N мс)**, когда RU достигает `eu.example.com:8443`.
3. Если красный — см. [Устранение неполадок](#устранение-неполадок).

---

### Фаза D — Тестовый пользователь и назначение цепочки

#### D.1 Создание пользователя

1. **Users** → **Add user**.
2. Email: `test@example.com`.
3. Запишите URL подписки на странице пользователя.

#### D.2 Назначение цепочки

1. Откройте пользователя **test@example.com**.
2. Прокрутите до секции **Multi-hop chain**.
3. Превью топологии: Client → Entry → Exit → Internet.
4. **Entry node:** выберите `entry-ru` или оставьте **Auto entry**.
5. **Exit node:** выберите `exit-eu` или оставьте **Auto exit**.
6. Нажмите **Save chain**.

Backend вызывает `PUT /api/users/{id}/chain`, проверяет роли, выполняет `core.Reload()` и перегенерирует `data/xray/config.json`.

#### D.3 Проверка egress IP в EU

Подключите клиента по ссылке подписки (только entry host). Затем:

```bash
curl -x socks5h://127.0.0.1:10808 https://ifconfig.me
# или через HTTP-прокси клиента
curl https://ifconfig.me
```

Возвращённый IP должен быть **публичным IP EU-сервера**, не RU.

---

## Справочник credentials JSON

Поле `credentials` exit-узла — **JSON-строка** в БД. Парсится `models.ParseNodeCredentials()` и рендерится в шаблон Xray outbound.

### Пример 1 — Vision + Reality (рекомендуется для hop RU→EU)

```json
{
  "uuid": "550e8400-e29b-41d4-a716-446655440000",
  "encryption": "none",
  "flow": "xtls-rprx-vision",
  "security": "reality",
  "public_key": "XyZ9wVuTsRqPoNmLkJiHgFeDcBa9876543210ZyXwVuTs",
  "short_id": "b2c3d4e5",
  "fingerprint": "firefox",
  "network": "tcp",
  "sni": "www.cloudflare.com"
}
```

| Поле | Значение | Соответствие в сгенерированном outbound |
|-------|-------|-------------------------------|
| `uuid` | Relay user UUID на EU | `settings.vnext[].users[].id` |
| `flow` | `xtls-rprx-vision` | `settings.vnext[].users[].flow` |
| `security` | `reality` | `streamSettings.security` |
| `public_key` | EU Reality public key | `realitySettings.publicKey` |
| `short_id` | EU short ID | `realitySettings.shortId` |
| `fingerprint` | `firefox` | `realitySettings.fingerprint` |
| `network` | `tcp` | `streamSettings.network` |
| `sni` | Опционально; по умолчанию `address` exit-узла | `realitySettings.serverName` |

### Пример 2 — XHTTP + Reality (hop через XHTTP)

```json
{
  "uuid": "660e8400-e29b-41d4-a716-446655440001",
  "encryption": "none",
  "security": "reality",
  "public_key": "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789AbCdEfGh",
  "short_id": "c3d4e5f6",
  "fingerprint": "firefox",
  "network": "xhttp",
  "path": "/api/v1/relay",
  "mode": "stream-one",
  "sni": "www.cloudflare.com"
}
```

EU inbound должен открывать XHTTP с тем же `path` и `mode`. Для XHTTP поле `flow` не нужно.

### Пример 3 — VLESS + TLS без Reality на hop

```json
{
  "uuid": "770e8400-e29b-41d4-a716-446655440002",
  "encryption": "none",
  "security": "tls",
  "network": "tcp",
  "sni": "eu.example.com",
  "fingerprint": "firefox"
}
```

Нужен валидный TLS-сертификат на EU inbound, совпадающий с `sni`. Реже для межузловых hop, чем Reality.

### Полный справочник полей

| Поле | Обязательно | По умолчанию | Описание |
|-------|----------|---------|-------------|
| `uuid` | Да (VLESS) | — | Relay user UUID на exit-сервере |
| `encryption` | Нет | `none` | Шифрование VLESS (`EncryptionOrDefault()`) |
| `flow` | Для Vision | — | напр. `xtls-rprx-vision` |
| `security` | Нет | `none` | `reality`, `tls` или `none` (`SecurityOrDefault()`) |
| `public_key` | Для Reality | — | Публичный Reality-ключ exit inbound (`pbk`) |
| `short_id` | Для Reality | — | Short ID exit inbound |
| `sni` | Нет | `address` exit-узла | TLS/Reality SNI в outbound |
| `fingerprint` | Нет | `firefox` | uTLS fingerprint (`FingerprintOrDefault()`) |
| `network` | Нет | `tcp` | `tcp` или `xhttp` (`NetworkOrDefault()`) |
| `path` | Для XHTTP | — | XHTTP path (должен совпадать с EU inbound) |
| `mode` | Для XHTTP | — | Режим XHTTP, обычно `stream-one` |

Источник схемы: [`backend/internal/models/node.go`](../backend/internal/models/node.go).

---

## Анатомия Xray-конфига

Когда `BuildMultihopData` возвращает `Enabled: true`, шаблон xray добавляет outbound и секцию routing.

### Логика генерации

Из `multihop.go`:

1. `BuildMultihopData` выполняется только если `multihop.IsEntryNode()` и в БД есть хотя бы один exit-узел.
2. Для каждого **активного пользователя** с разрешённым exit (`ResolveUserExitNode`) создаётся outbound, привязанный к ID exit-узла.
3. Тег outbound = `exit-<name>` через `Node.OutboundTag()`.
4. Email пользователей с одним exit группируются в одно правило routing на `exit-<name>-chain`.

### Фрагмент шаблона — VLESS outbound на exit

Из `xray.json.tmpl` (строки 169–201):

```json
{
  "protocol": "vless",
  "tag": "exit-exit-eu",
  "settings": {
    "vnext": [{
      "address": "eu.example.com",
      "port": 8443,
      "users": [{
        "id": "550e8400-e29b-41d4-a716-446655440000",
        "encryption": "none",
        "flow": "xtls-rprx-vision"
      }]
    }]
  },
  "streamSettings": {
    "network": "tcp",
    "security": "reality",
    "realitySettings": {
      "serverName": "eu.example.com",
      "fingerprint": "firefox",
      "publicKey": "EU_PUBLIC_KEY",
      "shortId": "b2c3d4e5"
    }
  }
}
```

### Фрагмент шаблона — chained freedom outbound

```json
{
  "protocol": "freedom",
  "tag": "exit-exit-eu-chain",
  "settings": {
    "domainStrategy": "AsIs"
  },
  "proxySettings": {
    "tag": "exit-exit-eu"
  }
}
```

### Фрагмент шаблона — routing по пользователям

```json
"routing": {
  "domainStrategy": "AsIs",
  "rules": [{
    "type": "field",
    "user": ["test@example.com"],
    "outboundTag": "exit-exit-eu-chain"
  }]
}
```

**Важно:** пользователи без разрешённого exit **не** попадают в multihop routing и используют outbound `direct` (egress RU).

### Просмотр живого конфига

```bash
# Pretty-print outbounds and routing
jq '.outbounds[] | select(.tag | startswith("exit-"))' data/xray/config.json
jq '.routing' data/xray/config.json

# Validate syntax
docker compose exec xray-core xray run -test -c /etc/xray/config.json
```

---

## Руководство по UI панели (`/nodes`)

URL: `http://<ru-host>:8888/nodes`

### Структура страницы

| Секция | Описание |
|---------|-------------|
| **Header** | Заголовок «Multi-hop nodes» и кнопка **Add node** |
| **Topology** | Компонент `ChainTopology`: Client → Entry → Exit → Internet. Показывает **активные** entry/exit с наименьшим priority в авто-режиме |
| **Entry nodes table** | Все узлы с `role: entry` |
| **Exit nodes table** | Все узлы с `role: exit` |

### Колонки и действия таблицы

| Колонка / действие | Значение |
|-----------------|---------|
| Name | Уникальный slug; имена exit определяют outbound-теги |
| Address:Port | Цель health-check и (для exit) outbound dial |
| Region | Информационная метка (напр. RU, EU) |
| Active / Inactive | Переключатель; неактивные узлы пропускаются в `ResolveUserEntryNode` / `ResolveUserExitNode` |
| Health check | `GET /api/nodes/{id}/health` — TCP dial, таймаут 5 с |
| Edit | Открывает модальное окно `NodeForm` |
| Delete | Удаляет узел; очищает `entry_node_id` / `exit_node_id` у затронутых пользователей |

### Интерпретация health-check

| Результат | Значение | Следующий шаг |
|--------|---------|-----------|
| `TCP OK (42ms)` | Порт открыт с хоста панели/backend | **Не** доказывает работу VLESS/Reality — проверьте живым трафиком |
| `connection refused` | На порту ничего не слушает | Запустите EU Xray; проверьте порт в конфиге |
| `i/o timeout` | Блокировка файрволом или маршрутизацией | Откройте EU firewall для IP RU |
| `address is empty` | Запись узла неполная | Отредактируйте узел, укажите address (адрес) |

### Активные и неактивные

- **Неактивный entry:** пропускается при авторазрешении; явный `entry_node_id` на неактивный узел откатывается к `GetBestEntryNode()`.
- **Неактивный exit:** пропускается при авторазрешении; пользователи с явным неактивным exit откатываются к `GetBestExitNode()`; если нет активных — multihop routing для них не создаётся.

### Зачем создавать entry-узел?

Entry-узлы отделяют `core.public_host` от клиентского endpoint. Используйте их когда:

- DNS указывает на балансировщик, но в ссылках нужен конкретный hostname.
- Назначаете разным пользователям разные entry IP в одной панели.

---

## Назначение цепочки пользователю

### Рабочий процесс в UI (`/users/:id`)

1. Откройте страницу пользователя.
2. Секция **Multi-hop chain** (`UserChainSection`):
   - Мини-диаграмма топологии обновляется при смене выбора.
   - **Auto entry** / **Auto exit** — пустое значение → активный узел с наименьшим priority.
   - **Save chain** → `PUT /api/users/{id}/chain`.

### Порядок разрешения (код)

`ResolveUserExitNode` (`db/nodes.go`):

1. Если задан `user.exit_node_id` → загрузить узел; вернуть, если активен и роль `exit`.
2. Иначе → `GetBestExitNode()` (активный exit с наименьшим priority).

Та же логика для entry через `ResolveUserEntryNode`.

### API — привязка цепочки

```bash
curl -s -X PUT http://localhost:8888/api/users/1/chain \
  -H "X-API-Key: YOUR_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "entry_node_id": 1,
    "exit_node_id": 2
  }'
```

Пример ответа:

```json
{
  "id": 1,
  "uuid": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "email": "test@example.com",
  "traffic_gb": 50,
  "used_gb": 0,
  "expires_at": "2026-10-08T08:00:00Z",
  "active": true,
  "entry_node_id": 1,
  "exit_node_id": 2,
  "created_at": "2026-09-08T08:00:00Z",
  "subscription_token": "abc123...",
  "subscription_url": "http://ru.example.com:8888/api/subscription/abc123..."
}
```

### API — авто-режим (сброс явных привязок)

```bash
curl -s -X PUT http://localhost:8888/api/users/1/chain \
  -H "X-API-Key: YOUR_KEY" \
  -H "Content-Type: application/json" \
  -d '{"clear": true}'
```

### API — через обновление пользователя

```bash
curl -s -X PUT http://localhost:8888/api/users/1 \
  -H "X-API-Key: YOUR_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "entry_node_id": 1,
    "exit_node_id": 2
  }'
```

Установите `"clear_chain": true`, чтобы обнулить оба ID узлов.

### Ответы с ошибками

| HTTP | Тело | Причина |
|------|------|-------|
| 400 | `invalid entry node` | ID отсутствует, неверная роль или не `entry` |
| 400 | `invalid exit node` | ID отсутствует, неверная роль или не `exit` |
| 404 | `record not found` | Пользователь с таким ID не существует |

---

## Подписка и поведение клиента

### Что содержат ссылки

`BuildSubscriptionLinks` вызывает `ResolveClientEndpoint`, затем `GetClientLinkProfiles`:

- Host = address entry-узла **или** `core.public_host`
- Port = порт entry-узла **или** `core.listen_port`
- User UUID = UUID конечного пользователя (не relay UUID)
- Stealth params = Reality-ключи, paths, modes **RU entry**

Hostname exit, relay UUID и Reality-ключи EU **никогда** не включаются.

Пример формата XHTTP-ссылки:

```
vless://USER_UUID@ru.example.com:443?encryption=none&type=xhttp&security=reality&sni=www.microsoft.com&fp=firefox&pbk=RU_PUBLIC_KEY&sid=a1b2c3d4&path=%2Fapi%2Fv1%2Fdata&mode=stream-one#test%40example.com-xhttp
```

### Endpoint подписки

```bash
curl -s "http://ru.example.com:8888/api/subscription/TOKEN" | base64 -d
```

Возвращает ссылки, разделённые переводом строки. Декодируйте и проверьте, что host — `ru.example.com`, а не `eu.example.com`.

### RioNexTunnel / `GET /api/client/config`

`BuildClientConfig` устанавливает:

```json
{
  "config_hash": "...",
  "servers": [{
    "protocol": "vless",
    "host": "ru.example.com",
    "port": 443,
    "id": "USER_UUID"
  }],
  "profiles": [ /* stealth profiles with entry host/port */ ],
  "inbounds": { "socks5": { "port": 10808, "auth": "none" } },
  "dns": { "servers": ["1.1.1.1", "8.8.8.8"] }
}
```

RioNexTunnel подключается **только к entry**. Мульти-хоп прозрачен — цепочка формируется внутри RU Xray после подключения клиента.

### Поведение `ResolveClientEndpoint`

```go
func ResolveClientEndpoint(publicHost string, listenPort int, user models.User, entry *models.Node) ClientEndpoint {
    if entry != nil {
        port := entry.Port
        if port <= 0 {
            port = listenPort
        }
        return ClientEndpoint{Host: entry.Address, Port: port}
    }
    return ClientEndpoint{Host: publicHost, Port: listenPort}
}
```

Параметр `user` зарезервирован для будущей per-user логики endpoint; сейчас не используется.

---

## Stealth и мульти-хоп вместе

Типичный продакшен-стек:

| Участок | Транспорт | Порт | Назначение |
|-----|-----------|------|---------|
| Клиент → RU | VLESS + Reality + XHTTP (`stream-one`) | 443 | Защита от DPI на первом hop |
| RU → EU | VLESS + Reality + Vision | 8443 | Эффективный межузловой релей |

### Чеклист конфигурации (RU entry)

- [ ] `core.stealth.enabled: true`
- [ ] RU Reality-ключи сгенерированы (`xray x25519`) — **отличные** от EU hop keys
- [ ] `core.stealth.xhttp.enabled: true`, `mode: stream-one`
- [ ] `core.stealth.vision.enabled: true`, если предлагаете Vision-профиль клиентам
- [ ] `core.multihop.enabled: true`, `local_role: entry`
- [ ] Credentials exit-узла совпадают с EU inbound (relay UUID, EU public key, short ID, flow)

### Чеклист конфигурации (EU exit)

- [ ] Отдельная пара Reality-ключей от RU
- [ ] Vision inbound на порту, совпадающем с `port` exit-узла
- [ ] Relay user UUID активен
- [ ] `core.multihop.enabled: false`
- [ ] Файрвол разрешает **только IP RU** на relay-порту (рекомендуется)

### Что видит DPI

- **Клиент ↔ RU:** трафик Reality, имитирующий TLS к CDN.
- **RU ↔ EU:** серверный VLESS (часто тоже Reality) — не виден клиентскому DPI.
- **Сайты:** IP EU-сервера как источник.

---

## Примеры конфигурации

### RU entry — полный `backend/config.yaml`

См. [Фаза A.5](#a5-настройка-ru-backendconfigyaml-построчно) для аннотированного примера.

### EU exit — полный `backend/config.yaml`

См. [Вариант B1](#вариант-b1--полный-rionexgate-на-eu-рекомендуется).

### Добавление multihop в `config.example.yaml`

В стандартном примере multihop отсутствует. Добавьте под `core:`:

```yaml
  multihop:
    enabled: true
    local_role: entry    # только на RU
```

---

## Справочник API

Базовый URL: `http://localhost:8888/api` (nginx) или `http://localhost:8080/api` (backend).  
Заголовок авторизации: `X-API-Key: <server.api_key>`

| Метод | Endpoint | Описание |
|--------|----------|-------------|
| GET | `/nodes` | Список всех узлов (по priority) |
| POST | `/nodes` | Создать узел |
| GET | `/nodes/{id}` | Получить узел |
| PUT | `/nodes/{id}` | Обновить узел |
| DELETE | `/nodes/{id}` | Удалить узел; очистить привязки цепочек |
| GET | `/nodes/{id}/health` | TCP health-check |
| PUT | `/users/{id}/chain` | Установить или сбросить цепочку |
| GET | `/users/{id}` | Детали пользователя с `entry_node_id`, `exit_node_id` |
| GET | `/subscription/{token}` | Base64-ссылки подписки |
| GET | `/client/config?token=...` | JSON-конфиг RioNexTunnel |

### Список узлов

```bash
curl -s -H "X-API-Key: YOUR_KEY" http://localhost:8888/api/nodes | jq .
```

### Создание exit-узла (полные credentials)

```bash
curl -s -X POST http://localhost:8888/api/nodes \
  -H "X-API-Key: YOUR_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "exit-eu",
    "address": "eu.example.com",
    "port": 8443,
    "role": "exit",
    "protocol": "vless",
    "region": "EU",
    "priority": 10,
    "active": true,
    "credentials": "{\"uuid\":\"550e8400-e29b-41d4-a716-446655440000\",\"flow\":\"xtls-rprx-vision\",\"security\":\"reality\",\"public_key\":\"EU_PUBLIC_KEY\",\"short_id\":\"b2c3d4e5\",\"fingerprint\":\"firefox\",\"network\":\"tcp\"}"
  }'
```

### Создание entry-узла

```bash
curl -s -X POST http://localhost:8888/api/nodes \
  -H "X-API-Key: YOUR_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "entry-ru",
    "address": "ru.example.com",
    "port": 443,
    "role": "entry",
    "region": "RU",
    "priority": 10,
    "active": true
  }'
```

OpenAPI: `GET /api/docs` — ищите `Nodes` и `UpdateUserChain`.

---

## Процедуры проверки

### 1. Проверка health-check в панели

Узлы → **Health check** у exit-узла. Ожидайте `TCP OK`.

### 2. Содержимое подписки

```bash
TOKEN="user_subscription_token"
curl -s "http://localhost:8888/api/subscription/$TOKEN" | base64 -d | head -5
```

Проверьте: host — entry; нет `eu.example.com`; присутствует user UUID.

### 3. Тест Xray-конфига

```bash
docker compose exec xray-core xray run -test -c /etc/xray/config.json
```

Ожидайте: `Configuration OK`.

### 4. Наличие тегов outbound

```bash
grep -E 'exit-exit-eu|exit-exit-eu-chain' data/xray/config.json
```

### 5. Живой egress IP

Через подключённого клиента:

```bash
curl https://ifconfig.me
curl https://ipinfo.io/country
```

Должна отображаться страна/IP EU.

### 6. Лог доступа Xray (отладка)

Временно установите `"loglevel": "debug"` в сгенерированном конфиге (или через правку шаблона), reload, наблюдайте межузловое соединение:

```bash
docker compose logs -f xray-core
```

### 7. Состояние цепочки через API

```bash
curl -s -H "X-API-Key: YOUR_KEY" http://localhost:8888/api/users/1 | jq '{email, entry_node_id, exit_node_id}'
```

---

## Устранение неполадок

| # | Симптом | Вероятная причина | Диагностика | Решение |
|---|---------|--------------|-----------|-----|
| 1 | Трафик выходит с RU IP | Нет exit; multihop выкл.; нет активного exit | `jq '.routing' data/xray/config.json` пуст? | Включите multihop; создайте exit; назначьте пользователю |
| 2 | Нет `exit-*` outbound в конфиге | `core.type: sing-box` | Проверьте `core.type` | Переключите на `xray` |
| 3 | Нет outbound при xray | Ни один пользователь не разрешается к exit | Список `exit_node_id` пользователей; проверьте активные exit | Назначьте exit; активируйте узел |
| 4 | Ошибка API `invalid exit node` | Неверная роль или ID | `GET /api/nodes/{id}` | Установите `role: exit` |
| 5 | Ошибка API `invalid entry node` | Узел с ролью exit | Проверьте роль узла | Создайте entry-узел |
| 6 | RU не достигает EU | Файрвол | `nc -zv eu.example.com 8443` с RU | Откройте порт; проверьте security groups |
| 7 | Health OK, VLESS не работает | Несовпадение credentials | Сравните JSON с EU inbound | Исправьте uuid, pbk, sid, flow |
| 8 | Ошибка Vision flow | Нет `flow` на hop | Проверьте outbound в config.json | Добавьте `flow: xtls-rprx-vision` |
| 9 | XHTTP hop не работает | Несовпадение path/mode | Сравните path/mode EU и credentials | Согласуйте обе стороны |
| 10 | Неверный SNI на hop | По умолчанию address exit | Установите `credentials.sni` | Совпадите с EU `serverNames` |
| 11 | Клиент показывает EU host | Ручная ссылка или неверная панель | Декодируйте подписку | Используйте только ссылки панели |
| 12 | Клиент показывает неверный RU host | Устаревший entry-узел | `GET /api/users/1` | Обновите address entry-узла |
| 13 | Дублирующиеся outbound-теги | Дублирующееся `name` узла | `GET /api/nodes` | Переименуйте — имена уникальны |
| 14 | Цепочка работала, перестала | Exit деактивирован | Проверьте переключатель active | Реактивируйте или переназначьте |
| 15 | Relay user отключён на EU | `active: false` у пользователя | Список пользователей EU-панели | Активируйте relay user |
| 16 | `multihop.enabled: true`, но нет routing | `local_role` не `entry` | Проверьте конфиг | Установите `local_role: entry` |
| 17 | Reload конфига не удался | Невалидный JSON в credentials | Логи backend | Исправьте JSON credentials |
| 18 | Высокая задержка | Расстояние RU↔EU | Мс health-check | Выберите ближайший EU POP |
| 19 | Часть пользователей на EU | Смешанные назначения | Per-user `exit_node_id` | Стандартизируйте или используйте авто |
| 20 | Удаление exit сломало пользователей | Ожидаемо — ID очищены | У пользователей null exit | Переназначьте; авто выберет новый лучший |

**Формат credentials:** валидный JSON в строковом поле `credentials`. Экранируйте кавычки в curl (`\"`) или используйте `-d @exit-node.json`.

---

## Заметки по безопасности

- **Credentials exit — секрет.** Они дают доступ к EU inbound. Храните только в БД entry-сервера; ограничьте API key и доступ к панели.
- **Не публикуйте exit-узел в подписках, QR и клиентских конфигах.** RioNexGate намеренно скрывает exit от клиентов — не обходите это ручными ссылками.
- **Используйте отдельный relay UUID** на EU, не UUID конечных пользователей.
- **Раздельные Reality keypair** для RU (клиенты) и EU (hop).
- **TLS для панели** на entry-сервере (см. [README.ru.md](../README.ru.md)).
- **Файрвол EU relay-порта** только для IP RU, когда возможно.
- При удалении exit-узла `exit_node_id` у затронутых пользователей очищается автоматически.
- Смените `server.api_key` с дефолтного перед продакшеном.

---

## Продакшен-чеклист

- [ ] RU: `core.type: xray`
- [ ] RU: `core.multihop.enabled: true`
- [ ] RU: `core.multihop.local_role: entry`
- [ ] RU: пара Reality-ключей сгенерирована и настроена
- [ ] RU: stealth-пресеты проверены (чеклист [stealth.ru.md](stealth.ru.md))
- [ ] EU: relay inbound слушает документированный порт
- [ ] EU: relay user создан и активен
- [ ] EU: отдельные Reality-ключи от RU
- [ ] RU exit-узел: credentials точно совпадают с EU inbound
- [ ] RU exit-узел: `active: true`
- [ ] RU entry-узел: клиентский `address:port` верен
- [ ] RU → EU: TCP доступен (health-check или `nc`)
- [ ] Пользователи: `exit_node_id` назначен или настроен авто exit
- [ ] Пользователи: `entry_node_id` назначен или `public_host` верен
- [ ] `xray run -test` проходит на entry
- [ ] Конфиг содержит `exit-<name>` и `exit-<name>-chain`
- [ ] Правила routing сопоставляют email пользователей с тегом `-chain`
- [ ] Подписка показывает только entry host
- [ ] Тест egress IP показывает EU
- [ ] API key панели сменён с дефолтного
- [ ] HTTPS панели включён (или доступ только через VPN)
- [ ] EU-панель за файрволом или не развёрнута
- [ ] EU relay-порт ограничен IP RU
- [ ] Мониторинг здоровья контейнера xray-core
- [ ] Запланирован бэкап `data/rionexgate.db`
- [ ] Задокументированы relay UUID и ID узлов для восстановления

---

## FAQ

**В: Могут ли клиенты подключаться напрямую к EU exit?**  
О: Технически да, если открыть EU inbound — но это обходит мульти-хоп. Блокируйте прямой доступ клиентов к EU; разрешайте только IP RU.

**В: Нужна ли запись entry-узла?**  
О: Нет. Без неё ссылки используют `core.public_host` + `listen_port`. Entry-узлы рекомендуются для явного клиентского endpoint.

**В: Может ли один пользователь использовать EU exit, а другой — прямой RU?**  
О: Пользователи без разрешённого exit используют outbound `direct` (egress RU). С назначенным exit — цепочку. Можно смешивать.

**В: Сколько exit-узлов можно зарегистрировать?**  
О: Неограниченно. Одна пара outbound (`exit-<name>`, `exit-<name>-chain`) на exit-узел с хотя бы одним пользователем.

**В: Что при удалении exit-узла?**  
О: Узел удаляется; `exit_node_id` очищается у затронутых пользователей. Они переходят на авто exit или без цепочки.

**В: Проверяет ли health-check Reality?**  
О: Нет. Только TCP dial (`checkNodeTCP`, таймаут 5 с).

**В: Можно ли VMess/Trojan для hop?**  
О: Поле `protocol` передаётся в шаблон, но схема credentials и UI ориентированы на VLESS. Рекомендуется VLESS + Reality.

**В: Почему тег outbound `exit-exit-eu`?**  
О: Префикс `exit-` плюс поле `name` узла (`OutboundTag()`).

**В: Работает ли sing-box для мульти-хопа?**  
О: Нет. `singbox.json.tmpl` игнорирует данные `Multihop`. Используйте Xray на entry.

**В: Можно ли мульти-хоп с AWG (WireGuard)?**  
О: Нет. AWG — отдельный клиентский транспорт; цепочки мульти-хоп — серверные маршруты Xray VLESS.

**В: Поможет ли фрагментация на RU Reality inbound?**  
О: Нет. Фрагментация только на опциональном TLS inbound из-за бага upstream Xray. См. [Ограничения](#ограничения).

**В: Как мигрировать с однохопового на мульти-хоп?**  
О: Включите multihop, зарегистрируйте exit, назначьте пользователям, reload. Клиентские ссылки не меняются (entry); egress IP станет EU.

---

## Ограничения

| Ограничение | Подробности |
|------------|--------|
| **sing-box** | `generateSingboxConfig` передаёт `Multihop` в шаблон, но `singbox.json.tmpl` не рендерит chain outbound. Entry должен использовать `core.type: xray`. |
| **AWG + multi-hop** | AmneziaWG (`core.stealth.awg`) — альтернативный клиентский транспорт. Не интегрируется с VLESS chain routing. Отключите AWG для мульти-хопа. |
| **Fragmentation + Reality** | `FragmentationRealityLimitation` в коде: Xray `finalmask.fragment` на REALITY inbound падает (подтверждено до v26.7.28). Фрагментация только на опциональном VLESS+TLS inbound. |
| **TCP-only health** | `/api/nodes/{id}/health` не проверяет VLESS, UUID или Reality handshake. |
| **UI credentials fields** | Форма узла показывает только UUID, public key, short ID. Полный JSON (flow, network, path) — через API. |
| **Stats on sing-box** | Сбор статистики трафика только для Xray (`fetchUserStats`). |
| **Single routing domain** | Нет split routing по доменам в multihop — весь трафик пользователя через один exit. |

---

## Полные примеры API запросов/ответов

### GET `/api/nodes/2` — детали exit-узла

Ответ:

```json
{
  "id": 2,
  "name": "exit-eu",
  "address": "eu.example.com",
  "port": 8443,
  "active": true,
  "role": "exit",
  "protocol": "vless",
  "credentials": "{\"uuid\":\"550e8400-e29b-41d4-a716-446655440000\",\"flow\":\"xtls-rprx-vision\",\"security\":\"reality\",\"public_key\":\"EU_PUBLIC_KEY\",\"short_id\":\"b2c3d4e5\",\"fingerprint\":\"firefox\",\"network\":\"tcp\"}",
  "region": "EU",
  "priority": 10
}
```

### PUT `/api/nodes/2` — обновление credentials после ротации ключей EU

```bash
curl -s -X PUT http://localhost:8888/api/nodes/2 \
  -H "X-API-Key: YOUR_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "credentials": "{\"uuid\":\"550e8400-e29b-41d4-a716-446655440000\",\"flow\":\"xtls-rprx-vision\",\"security\":\"reality\",\"public_key\":\"NEW_EU_PUBLIC_KEY\",\"short_id\":\"newshort\",\"fingerprint\":\"firefox\",\"network\":\"tcp\"}"
  }'
```

Вызывает `core.Reload()` — Xray подхватывает новый outbound без смены клиентских ссылок.

### GET `/api/nodes/2/health` — пример ошибки

```json
{
  "reachable": false,
  "check_type": "tcp",
  "latency_ms": 5002,
  "error": "dial tcp 203.0.113.50:8443: i/o timeout"
}
```

### POST `/api/users` затем chain — сквозной пример

```bash
# Создать пользователя
curl -s -X POST http://localhost:8888/api/users \
  -H "X-API-Key: YOUR_KEY" \
  -H "Content-Type: application/json" \
  -d '{"email":"chain-test@example.com","traffic_gb":10,"expire_days":30}'

# Назначить цепочку (предположим user id=3, entry=1, exit=2)
curl -s -X PUT http://localhost:8888/api/users/3/chain \
  -H "X-API-Key: YOUR_KEY" \
  -H "Content-Type: application/json" \
  -d '{"entry_node_id":1,"exit_node_id":2}'
```

---

## Пример standalone EU XHTTP inbound

Если hop RU→EU использует XHTTP вместо Vision, standalone-конфиг EU:

```json
{
  "log": { "loglevel": "warning" },
  "inbounds": [{
    "tag": "relay-xhttp",
    "listen": "0.0.0.0",
    "port": 443,
    "protocol": "vless",
    "settings": {
      "clients": [{
        "id": "660e8400-e29b-41d4-a716-446655440001",
        "email": "relay@eu.internal",
        "level": 0
      }],
      "decryption": "none"
    },
    "streamSettings": {
      "network": "xhttp",
      "security": "reality",
      "realitySettings": {
        "show": false,
        "dest": "www.cloudflare.com:443",
        "xver": 0,
        "serverNames": ["www.cloudflare.com"],
        "privateKey": "EU_REALITY_PRIVATE_KEY",
        "shortIds": ["c3d4e5f6"]
      },
      "xhttpSettings": {
        "path": "/api/v1/relay",
        "mode": "stream-one"
      }
    },
    "sniffing": {
      "enabled": true,
      "destOverride": ["http", "tls", "quic"]
    }
  }],
  "outbounds": [{ "protocol": "freedom", "tag": "direct" }]
}
```

Соответствующий exit-узел на RU: `port: 443`, credentials с `network: xhttp`, `path`, `mode`, без `flow`.

---

## Восстановление после сбоев

| Сценарий | Шаги восстановления |
|----------|------------------|
| Потеря БД RU | Восстановите `data/rionexgate.db` из бэкапа; `make up`; проверьте узлы и цепочки |
| Утечка relay UUID на EU | Создайте нового relay на EU; обновите credentials exit-узла; reload RU |
| Компрометация Reality-ключа EU | Новые ключи `xray x25519`; обновите конфиг EU и credentials exit на RU |
| Неверная страна egress | Проверьте `exit_node_id`; регион exit-узла и расположение EU |
| Массово неверный exit у пользователей | `PUT /users/{id}/chain` с `clear: true` для авто; исправьте priority exit |

---

## См. также

- [stealth.ru.md](stealth.ru.md) — Reality, XHTTP, Vision на entry hop
- [README.ru.md](../README.ru.md) — установка и Makefile
- [docs/README.md](README.md) — индекс документации
- OpenAPI: `GET /api/docs` — `Nodes` и `PUT /users/{id}/chain`
