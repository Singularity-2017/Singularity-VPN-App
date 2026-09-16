# Singularity VPN — Windows Client

## 1. Назначение

`Singularity VPN Windows Client` — нативное Windows-приложение Singularity VPN с минималистичным пользовательским интерфейсом и единым Client Core.

Первая версия ориентирована на сценарий:

> Установить → открыть → нажать одну кнопку → подключиться к оптимальному VPN.

Windows Client не должен содержать собственную реализацию бизнес-логики VPN. Вся прикладная логика должна находиться в `Client Core`, описанном в `CLIENT_CORE.md`.

---

# 2. Цели

Windows Client должен обеспечить:

- одно основное окно;
- большую кнопку Connect/Disconnect;
- автоматический выбор node;
- автоматический выбор protocol;
- отображение текущего состояния;
- отображение используемого сервера;
- отображение протокола;
- отображение RTT/диагностики;
- ручной выбор сервера;
- ручной выбор протокола;
- ручное переключение;
- автоматическое восстановление;
- Auto Switch;
- диагностику;
- работу в system tray;
- запуск вместе с Windows в будущем;
- безопасное выполнение привилегированных операций.

---

# 3. Не является целью MVP

Не требуется сразу реализовывать:

- сложный dashboard;
- встроенный marketplace;
- полноценный account management;
- сложную статистику;
- несколько окон;
- анимационный UI;
- собственный VPN protocol;
- собственный networking stack;
- реализацию server-side Smart Routing;
- управление VPS;
- админ-панель.

Главный сценарий должен оставаться простым:

```text
        ┌─────────────────────┐
        │                     │
        │     SINGULARITY     │
        │                     │
        │       CONNECT       │
        │                     │
        │    DE-01 · 43 ms    │
        │                     │
        └─────────────────────┘
```

---

# 4. Technology Stack

Основной стек Windows Client:

```text
Language:
    Go

Desktop framework:
    Wails v2

Frontend:
    web frontend supported by Wails

Core:
    existing Singularity VPN Client Core

VPN:
    existing protocol implementations / system clients

Platform:
    Windows
```

Go + Wails является предпочтительным вариантом для текущего проекта, поскольку основной backend/client logic уже написан или планируется на Go.

Не вводить Rust/Tauri или C#/.NET без отдельного архитектурного решения.

---

# 5. Главный архитектурный принцип

Wails является **presentation layer**.

```text
Wails UI
    │
    ▼
Wails bindings
    │
    ▼
Client Core
    │
    ├── Control Plane
    ├── Connection Manager
    ├── Protocol Manager
    ├── Diagnostics
    ├── Recovery
    └── State
```

Wails не должен напрямую:

- управлять WireGuard;
- запускать OpenVPN;
- менять routes;
- менять firewall;
- обращаться к Control Plane;
- реализовывать Auto Switch;
- определять оптимальный сервер.

Все эти действия выполняются Client Core или Platform Layer.

---

# 6. Структура приложения

Рекомендуемая структура:

```text
cmd/
    singularity-vpn/

internal/
    client/
        ...

    windows/
        ...

    ui/
        ...

    app/
        ...

frontend/
    ...
```

Фактическая структура проекта может отличаться в зависимости от существующей кодовой базы.

Главное правило:

```text
frontend → Wails bindings → Client Core
```

а не:

```text
frontend → Windows/VPN implementation
```

---

# 7. Wails Application Layer

Wails application object является адаптером между frontend и Core.

Концептуально:

```go
type App struct {
    client client.Client
}
```

Методы:

```go
Connect()
Disconnect()
Switch()
Status()
Test()
```

могут быть exposed в frontend через Wails.

Их задача — преобразовать frontend request в вызов Client Core.

---

# 8. Запрещённая логика в Wails App

Не допускается:

```go
func (a *App) Connect() error {
    // HTTP request
    // select server
    // generate config
    // execute wireguard
    // modify routes
    // verify connection
}
```

Wails App должен быть тонким:

```go
func (a *App) Connect() error {
    return a.client.Connect(...)
}
```

---

# 9. UI State

Frontend должен отображать состояние Core.

Основные состояния:

```text
Disconnected
Connecting
Connected
Degraded
Switching
Disconnecting
```

Frontend не должен самостоятельно вычислять эти состояния.

Core является источником истины.

---

# 10. Главная кнопка

Главная кнопка должна быть контекстной.

Пример:

```text
Disconnected:
    CONNECT

Connecting:
    CONNECTING...

Connected:
    DISCONNECT

Degraded:
    RECONNECTING...

Switching:
    SWITCHING...

Disconnecting:
    DISCONNECTING...
```

UI не должен позволять пользователю выполнять конфликтующие операции.

Например, во время `Connecting` повторный Connect должен быть либо заблокирован UI, либо безопасно обработан Core.

Безопасность должна обеспечиваться прежде всего Core, а не UI.

---

# 11. Основной экран

MVP:

```text
┌──────────────────────────────────┐
│ Singularity VPN             ⚙   │
│                                  │
│                                  │
│            CONNECT               │
│                                  │
│                                  │
│          Auto · DE-01            │
│             43 ms                │
│                                  │
│           WireGuard              │
│                                  │
└──────────────────────────────────┘
```

Не перегружать основной экран техническими деталями.

Дополнительная информация должна быть доступна через:

- server selector;
- details;
- diagnostics;
- settings.

---

# 12. Server Selection

Пользователь должен иметь возможность увидеть доступные nodes.

Например:

```text
Germany
    DE-01    43 ms
    DE-02    47 ms

Finland
    FI-01    52 ms

Poland
    PL-01    38 ms
```

Однако отображение latency не означает, что именно минимальный RTT является лучшим вариантом.

Итоговый рейтинг определяется Control Plane.

---

# 13. Protocol Selection

При необходимости пользователь может выбрать:

```text
Auto
WireGuard
OpenVPN UDP
OpenVPN TCP
Xray
```

Доступные варианты должны приходить от Control Plane/Client Core.

UI не должен предполагать, что все протоколы доступны на всех nodes.

---

# 14. Auto Mode

По умолчанию:

```text
Node:
    Auto

Protocol:
    Auto
```

При Connect:

```text
Client Core
    ↓
Control Plane
    ↓
Recommendation
    ↓
selected node/protocol
    ↓
connection
```

Пользователь может вообще не знать, какой протокол был выбран.

---

# 15. Manual Mode

Пользователь может выбрать:

```text
Node = DE-01
Protocol = WireGuard
```

Core выполняет подключение с заданными параметрами.

Manual mode не должен обходить:

- verification;
- state machine;
- diagnostics;
- error handling.

Рекомендуемое поведение:

```text
Manual selection
    ↓
Auto Switch disabled
```

если пользователь явно не включил автоматическое переключение.

---

# 16. Status

UI должен получать состояние через Core:

```text
Status()
```

Пример:

```text
Connected
Node: DE-01
Protocol: WireGuard
RTT: 43 ms
Duration: 01:42:18
```

При disconnect:

```text
Disconnected
```

Не нужно хранить независимое второе состояние в frontend, которое может расходиться с Core.

---

# 17. Events

Wails должен подписываться на события Client Core.

Пример:

```text
Core
 │
 ├── StateChanged
 ├── ConnectionEstablished
 ├── ConnectionLost
 ├── SwitchStarted
 ├── SwitchCompleted
 └── Error
        │
        ▼
Wails
        │
        ▼
Frontend
```

Это предпочтительнее постоянного polling.

Polling может использоваться для данных, где event-driven модель нецелесообразна.

---

# 18. System Tray

Windows Client должен поддерживать system tray.

Минимальные действия:

```text
Singularity VPN
    ├── Open
    ├── Connect / Disconnect
    ├── Switch
    ├── Test
    └── Exit
```

Закрытие основного окна не обязательно должно завершать клиент.

Предпочтительно:

```text
X window
    ↓
hide to tray
```

а отдельная команда `Exit` полностью завершает пользовательское приложение.

---

# 19. Background Operation

Auto Switch и monitoring не должны зависеть от открытого окна.

Например:

```text
User closes window
       ↓
Tray
       ↓
Client Core continues
       ↓
VPN remains connected
```

Если для этого потребуется отдельный Windows Service, архитектура должна позволять его добавить без изменения бизнес-логики Core.

---

# 20. Windows Service

Для privileged operations должна предусматриваться возможность отдельного сервиса:

```text
Singularity VPN.exe
        │
        │ IPC
        ▼
Singularity VPN Service
        │
        ├── VPN operations
        ├── routes
        ├── firewall
        ├── kill switch
        └── privileged networking
```

Причина:

> Пользовательский GUI не должен постоянно работать с административными привилегиями только потому, что VPN иногда требует их.

---

# 21. Service Boundary

Client Core не должен знать, работает ли Platform implementation:

```text
directly
```

или:

```text
through Windows Service
```

Например:

```go
type VPNPlatform interface {
    Connect(ctx context.Context, config ConnectionConfig) error
    Disconnect(ctx context.Context) error
    Status() VPNStatus
}
```

Реализация может использовать:

```text
local process
```

или:

```text
Windows Service IPC
```

без изменения Core.

---

# 22. IPC

Если Windows Service вводится, IPC должен быть локальным и защищённым.

Предпочтительно использовать подходящий Windows-native механизм, например named pipes.

Не использовать локальный HTTP server без необходимости.

Service должен проверять:

- кто вызывает API;
- допустимость операции;
- корректность параметров;
- состояние сервиса.

Не передавать секреты через небезопасные IPC-механизмы.

---

# 23. VPN Protocol Integration

Windows Client не должен самостоятельно реализовывать VPN-протокол.

Архитектура:

```text
Client Core
    ↓
Protocol Adapter
    ↓
Platform VPN implementation
    ↓
Windows VPN client/service
```

Для WireGuard и OpenVPN конкретный механизм интеграции должен быть определён при реализации соответствующих adapters.

Не предполагать заранее конкретный executable/service API, если это ещё не проверено на целевой Windows-среде.

---

# 24. WireGuard

Первый VPN protocol для Windows Client должен быть WireGuard, если существующая серверная и клиентская инфраструктура готова к этому.

Flow:

```text
Control Plane
    ↓
WireGuard config
    ↓
WireGuard Adapter
    ↓
Windows implementation
    ↓
Verification
    ↓
Connected
```

WireGuard-specific код не должен проникать в UI.

---

# 25. OpenVPN

OpenVPN добавляется через отдельный adapter:

```text
OpenVPN Adapter
```

Core не должен различать:

```text
WireGuard implementation details
OpenVPN implementation details
```

Он знает только:

```text
Protocol
Connect
Disconnect
Status
```

---

# 26. Future Xray/VLESS

Архитектура должна позволять:

```text
Xray
VLESS
Amnezia
```

добавлять новые adapters.

При этом UI автоматически может отображать новые protocols, если Core предоставляет их как доступные protocol capabilities.

---

# 27. Kill Switch

Kill Switch не должен быть реализован в frontend.

Это системная функция.

Архитектура:

```text
Client Core
    ↓
Platform
    ↓
Windows networking/firewall
```

Core принимает решение о desired state:

```text
KillSwitch = enabled
```

Platform выполняет системную настройку.

---

# 28. Network Changes

Windows Client должен учитывать изменения сети:

```text
Wi-Fi → Ethernet
Ethernet → Wi-Fi
Network lost
Network restored
Sleep
Resume
```

События сети должны поступать в Core через Platform Layer.

Пример:

```text
Network lost
    ↓
Core
    ↓
Connection degraded
    ↓
Recovery
```

Не реализовывать network monitoring непосредственно во frontend.

---

# 29. Sleep / Resume

После resume клиент должен проверить состояние VPN.

Пример:

```text
Windows Sleep
    ↓
Resume
    ↓
Platform reports network change
    ↓
Core checks connection
    ↓
Verification
    │
    ├── OK → Connected
    │
    └── fail → Recovery
```

---

# 30. Secure Storage

Windows Client не должен хранить секреты в:

```text
JSON
SQLite
plain text files
frontend localStorage
```

если эти данные являются credentials/secrets.

Использовать `SecureStore` Client Core.

Windows implementation должна использовать подходящий защищённый механизм Windows.

---

# 31. Logs

Логи должны быть доступны для диагностики.

Рекомендуемое расположение:

```text
%LOCALAPPDATA%\SingularityVPN\logs\
```

Конкретный путь может быть изменён при реализации.

Логи должны содержать:

```text
timestamp
level
component
event
session_id
node_id
protocol
error
```

Не логировать:

```text
private keys
tokens
passwords
full VPN configs
```

---

# 32. Diagnostics UI

Отдельный экран:

```text
Diagnostics

Control Plane       OK
Internet             OK
VPN                   OK

Node                  DE-01
Protocol              WireGuard

RTT                   43 ms
Packet loss            0%
DNS                    OK
```

Кнопка:

```text
RUN TEST
```

вызывает:

```text
Client Core → Diagnostics
```

---

# 33. Error Presentation

Frontend не должен показывать пользователю внутренние stack traces.

Например:

```text
Connection failed
```

с дополнительной информацией:

```text
Unable to establish WireGuard connection.
Try another protocol?
```

В diagnostics можно показать техническую информацию.

Ошибки должны поступать из typed error model Core.

---

# 34. Update System

Windows Client должен предусматривать обновление приложения.

Но update logic не должна смешиваться с VPN connection logic.

Отдельный компонент:

```text
Updater
```

может отвечать за:

- проверку новой версии;
- скачивание;
- проверку подписи;
- установку;
- restart.

Источник версий должен быть определён отдельно от Control Plane.

---

# 35. Code Signing

Production Windows builds должны быть подписаны code-signing certificate.

Это важно для:

- доверия Windows;
- SmartScreen;
- antivirus/EDR reputation;
- безопасного распространения.

Не считать использование Go/Wails само по себе гарантией отсутствия AV/EDR предупреждений.

Репутация зависит в том числе от:

- подписи;
- publisher identity;
- installer behavior;
- распространения;
- reputation;
- поведения приложения;
- наличия драйверов/служб;
- сетевой активности.

---

# 36. Installer

Установщик должен:

- установить приложение;
- установить необходимые компоненты;
- при необходимости установить Windows Service;
- зарегистрировать service;
- создать необходимые директории;
- корректно удалить/обновить приложение.

Установщик не должен удалять или изменять посторонние VPN-программы.

---

# 37. Existing VPN Software

На компьютере пользователя могут быть:

- WireGuard;
- OpenVPN;
- другие VPN clients.

Installer не должен автоматически удалять их.

Перед изменением системных компонентов необходимо определить:

```text
what exists
what Singularity owns
what Singularity needs
```

---

# 38. Ownership

Каждый созданный Singularity ресурс должен быть идентифицируем.

Например:

```text
Singularity VPN
    WireGuard tunnel
    service
    firewall rules
    routes
```

Не следует безусловно удалять все найденные WireGuard/OpenVPN resources.

Нужно удалять только ресурсы, которыми владеет Singularity.

---

# 39. Uninstall

Uninstall должен удалить:

- приложение;
- Singularity-owned service;
- Singularity-owned VPN resources;
- Singularity-owned firewall rules;
- Singularity-owned local state.

Не удалять:

- чужие VPN profiles;
- пользовательские данные других приложений;
- системные компоненты, не принадлежащие Singularity.

---

# 40. Configuration

Локальные настройки могут включать:

```text
Auto connect
Auto switch
Preferred protocol
Preferred node
Start with Windows
Minimize to tray
Kill switch
```

Однако серверные возможности и доступность должны определяться Control Plane.

---

# 41. Auto Connect

Опциональная функция:

```text
Windows starts
    ↓
Singularity Client
    ↓
Client Core
    ↓
Connect()
```

Auto Connect не должен обходить обычный connection flow.

---

# 42. Start With Windows

Если включено:

```text
Start with Windows
```

приложение может запускаться в tray.

Если Auto Connect включён:

```text
Application start
    ↓
Core.Connect()
```

Не выполнять VPN operations непосредственно из startup code.

---

# 43. Single Instance

Windows Client должен предотвращать запуск нескольких конфликтующих GUI instances.

Например:

```text
Instance #1
    ↓
owns user session

Instance #2
    ↓
focuses existing instance
```

Это не должно создавать две независимые connection sessions.

---

# 44. Threading

Wails UI thread не должен блокироваться длительными операциями.

Например:

```text
Connect()
```

может занимать секунды.

Frontend должен получать:

```text
Connecting...
```

через async/event mechanism.

Не блокировать UI ожиданием VPN startup.

---

# 45. Security Boundary

Основная модель:

```text
Unprivileged GUI
       │
       ▼
Client Core
       │
       ▼
Controlled privileged interface
       │
       ▼
Windows Service
```

Нельзя превращать Windows Service в произвольный privileged command executor.

Service должен предоставлять только ограниченный набор операций.

Например:

```text
CreateConnection
StartConnection
StopConnection
DeleteConnection
GetStatus
SetRoutingState
```

а не:

```text
ExecuteArbitraryCommand(command string)
```

---

# 46. Crash Recovery

Если GUI падает:

```text
GUI crash
    ↓
VPN connection
```

поведение должно быть определено отдельно.

Предпочтительно, чтобы VPN lifecycle не зависел от UI process, если используется Windows Service.

При отсутствии service в MVP поведение должно быть явно документировано.

---

# 47. Client Core Lifetime

Core должен иметь lifecycle:

```text
Create
    ↓
Initialize
    ↓
Ready
    ↓
Running
    ↓
Shutdown
```

Initialization может включать:

- loading state;
- initialization Control Plane;
- protocol discovery;
- platform initialization;
- event system.

---

# 48. Application Lifecycle

Windows application:

```text
Start
  ↓
Initialize Core
  ↓
Subscribe Events
  ↓
Show UI / Tray
  ↓
Core running
  ↓
User closes UI
  ↓
Tray/background
  ↓
Exit
  ↓
Shutdown Core
```

---

# 49. Telemetry

Telemetry должна быть минимальной и осознанной.

Не отправлять на сервер произвольные пользовательские данные.

Если Control Plane получает connection reports, они должны соответствовать API и privacy model проекта.

Минимально полезные данные:

```text
node
protocol
connect/disconnect result
latency
failure category
timestamp
client version
```

Точные поля определяются серверным API.

---

# 50. Versioning

Windows Client должен иметь собственную версию:

```text
Singularity VPN Client X.Y.Z
```

Версия передаётся Control Plane при необходимости.

Версия Client Core должна быть согласована с application versioning strategy.

---

# 51. Compatibility

Control Plane должен быть способен определить совместимость клиента.

Например:

```text
Client version:
    1.2.0

Supported API:
    v1
```

Если клиент слишком стар:

```text
Client update required
```

необходимо сообщать это как отдельное состояние, а не как generic connection error.

---

# 52. Project Structure

Предпочтительно:

```text
cmd/
    singularity-vpn/

internal/
    client/
        core/
        api/
        connection/
        protocols/
        routing/
        recovery/
        diagnostics/
        events/
        state/
        security/

    platform/
        windows/

    app/
        wails/

frontend/
    ...
```

Если Client Core уже существует в другом расположении, не переносить код только ради соответствия этому документу.

Существующая архитектура проекта имеет приоритет.

---

# 53. Dependency Injection

Windows application composition root должен собрать зависимости:

```text
Wails App
    │
    └── Client Core
           │
           ├── Control Plane
           ├── Protocol Manager
           ├── Diagnostics
           ├── Platform
           ├── State Store
           └── Secure Store
```

Конкретные реализации создаются в composition root.

Core не должен самостоятельно создавать глобальные singleton dependencies.

---

# 54. Testing

Обязательно тестировать отдельно:

## Client Core

Без Windows GUI.

```text
Connect
Disconnect
Switch
Recovery
Auto Switch
State transitions
Errors
```

## Windows Platform

На реальной Windows environment:

```text
VPN
routes
firewall
service
secure storage
network changes
sleep/resume
```

## Wails UI

Проверять:

```text
button states
event handling
display
tray
settings
```

UI-тесты не должны использоваться для проверки бизнес-логики Core.

---

# 55. MVP Roadmap

## Phase 1

```text
Wails
  ↓
Client Core
  ↓
Control Plane
  ↓
WireGuard
```

Функции:

- Connect;
- Disconnect;
- Status;
- Auto recommendation;
- basic diagnostics;
- events;
- basic settings.

---

## Phase 2

Добавить:

- OpenVPN UDP;
- OpenVPN TCP;
- manual node selection;
- manual protocol selection;
- Switch;
- recovery;
- Auto Switch;
- system tray.

---

## Phase 3

Добавить:

- Windows Service;
- kill switch;
- network change handling;
- sleep/resume recovery;
- auto connect;
- richer diagnostics.

---

## Phase 4

Добавить:

- Xray;
- VLESS;
- дополнительные protocols;
- advanced routing;
- multi-profile;
- additional platforms.

---

# 56. Architectural Invariants

Следующие правила являются обязательными:

1. Wails не содержит VPN business logic.
2. Frontend не управляет VPN напрямую.
3. CLI и GUI используют один Client Core.
4. Client Core не зависит от Wails.
5. Client Core не зависит от Windows API.
6. Protocol implementations изолированы adapters.
7. Control Plane доступен через interface.
8. Server-side Smart Routing не дублируется в клиенте.
9. Privileged Windows operations изолированы Platform Layer.
10. GUI не является источником истины о состоянии VPN.
11. Secrets не хранятся в обычном frontend/local state.
12. Singularity удаляет только принадлежащие ему системные ресурсы.
13. Новый protocol не требует переписывания GUI.
14. Новый UI не требует переписывания Client Core.
15. Windows Service может быть добавлен без изменения business logic Core.

---

# 57. Критерий качества

Хорошая реализация должна позволять заменить:

```text
Wails
```

на:

```text
CLI
```

или:

```text
другой GUI
```

без изменения Connection Manager.

Также должна позволять заменить:

```text
Windows direct implementation
```

на:

```text
Windows Service
```

без изменения Connection Manager.

И должна позволять добавить:

```text
Xray
```

без изменения UI.

Итог:

```text
                    CLIENT CORE
                         │
          ┌──────────────┼──────────────┐
          │              │              │
       Wails            CLI          Future UI
          │              │              │
          └──────────────┼──────────────┘
                         │
                  Platform Interface
                         │
                  ┌──────┴──────┐
                  │             │
             Windows        Future OS
                  │
             ┌────┴────┐
             │         │
          Direct     Service
             │         │
             └────┬────┘
                  │
             VPN Protocols
          ┌───────┼────────┐
          │       │        │
       WireGuard OpenVPN  Xray
```

**Главное правило реализации:**

> Windows Client — это интерфейс над Client Core, а не отдельный VPN-клиент с собственной логикой.

Любая новая функция должна сначала рассматриваться как функция Client Core или Platform Layer и только затем получать UI-представление.