# Singularity VPN — Client Core

## 1. Назначение

`Client Core` — основное прикладное ядро клиентского приложения Singularity VPN.

Он содержит всю клиентскую логику, необходимую для:

- подключения к VPN;
- отключения;
- выбора сервера;
- выбора протокола;
- получения рекомендаций от Control Plane;
- переключения между протоколами и серверами;
- автоматического восстановления соединения;
- диагностики;
- мониторинга состояния;
- локального состояния клиента;
- взаимодействия с локальной платформой;
- публикации событий для пользовательского интерфейса.

Client Core **не является GUI**, не является CLI и не является реализацией конкретного VPN-протокола.

Основная архитектурная цель:

> GUI, CLI и будущие интерфейсы должны использовать одну и ту же клиентскую логику через Client Core.

---

# 2. Архитектурный принцип

Общая схема:

```text
                     Singularity VPN Client
                              │
                ┌─────────────┴─────────────┐
                │                           │
           Windows GUI                    CLI
            Wails v2                singularity-vpn
                │                           │
                └─────────────┬─────────────┘
                              │
                         Client Core
                              │
          ┌───────────────────┼───────────────────┐
          │                   │                   │
     Control Plane       Connection          Diagnostics
          API              Manager                │
                              │                   │
                         Protocols             Tests
                              │
               ┌──────────────┼──────────────┐
               │              │              │
           WireGuard       OpenVPN          Xray
```

Client Core является центральным прикладным слоем.

Интерфейс пользователя должен быть тонким.

Протоколы должны подключаться через абстракции.

Платформенные детали должны быть скрыты за platform interfaces.

---

# 3. Границы ответственности

## 3.1 Client Core отвечает за

- lifecycle подключения;
- состояние подключения;
- orchestration;
- получение рекомендаций;
- выбор node/protocol согласно рекомендации;
- получение VPN-конфигурации;
- запуск и остановку protocol adapter;
- проверку результата подключения;
- reconnect;
- auto switch;
- diagnostics;
- события;
- локальное состояние;
- обработку ошибок;
- координацию Control Plane и локальной платформы.

## 3.2 Client Core не отвечает за

- отрисовку GUI;
- Wails bindings;
- отображение кнопок;
- реализацию HTTP UI;
- непосредственное выполнение WireGuard-команд;
- непосредственное выполнение OpenVPN-команд;
- реализацию Xray;
- конкретную реализацию Windows Service;
- хранение секретов в открытом виде;
- серверный Smart Routing;
- управление VPS;
- управление PostgreSQL;
- provisioning серверов.

---

# 4. Главная модель

Client Core должен рассматриваться как самостоятельное приложение-движок:

```text
Application UI
      │
      ▼
Client Core
      │
      ├── Control Plane
      ├── Protocols
      ├── Platform
      ├── Diagnostics
      └── State
```

UI только вызывает публичные операции Core и подписывается на события.

---

# 5. Публичный API Core

Конкретный API может изменяться в процессе реализации, но архитектурно Core должен предоставлять минимум:

```go
type Client interface {
    Connect(ctx context.Context, req ConnectRequest) error
    Disconnect(ctx context.Context) error
    Switch(ctx context.Context, target SwitchTarget) error

    Status() ConnectionStatus

    Test(ctx context.Context) (DiagnosticResult, error)

    Subscribe() <-chan Event
}
```

Дополнительные методы могут быть добавлены для:

- авторизации;
- получения профиля;
- получения списка nodes;
- получения доступных протоколов;
- изменения настроек;
- получения статистики.

Публичный API должен быть ориентирован на **намерения пользователя**, а не на внутренние действия.

Хорошо:

```text
Connect()
Disconnect()
Switch()
Test()
Status()
```

Плохо:

```text
CreateWireGuardConfig()
ExecuteOpenVPNCommand()
SetWindowsRoute()
ModifyFirewallRule()
```

Последние являются внутренними механизмами.

---

# 6. Connect

`Connect()` является orchestration-операцией.

Типовой flow:

```text
Connect()
   │
   ├── проверить текущее состояние
   │
   ├── получить recommendation
   │
   ├── определить node
   │
   ├── определить protocol
   │
   ├── запросить connection config
   │
   ├── передать config Protocol Adapter
   │
   ├── дождаться установления соединения
   │
   ├── выполнить verification/self-test
   │
   └── установить Connected
```

Если пользователь не указал конкретный сервер/протокол, Core должен использовать рекомендацию Control Plane.

Если пользователь явно выбрал node/protocol, соответствующий override должен быть сохранён в `ConnectRequest`.

---

# 7. Connect Request

Пример концептуальной модели:

```go
type ConnectRequest struct {
    NodeID   *string
    Protocol *string
    Mode     ConnectMode
}
```

Возможные режимы:

```text
auto
manual
```

В `auto`:

```text
Client → Control Plane → Recommendation
```

В `manual`:

```text
Client → requested node/protocol
```

Даже в manual mode Core должен проверять доступность выбранного варианта.

---

# 8. Control Plane

Control Plane является удалённым источником клиентских рекомендаций и конфигурации.

Client Core взаимодействует с ним через интерфейс:

```go
type ControlPlane interface {
    Authenticate(
        ctx context.Context,
        credentials Credentials,
    ) error

    GetProfile(
        ctx context.Context,
    ) (Profile, error)

    Recommend(
        ctx context.Context,
        req RecommendRequest,
    ) (Recommendation, error)

    GetConnectionConfig(
        ctx context.Context,
        nodeID string,
        protocol string,
    ) (ConnectionConfig, error)

    ReportConnection(
        ctx context.Context,
        report ConnectionReport,
    ) error
}
```

Фактический HTTP API должен соответствовать серверной `API_SPEC.md`.

Client Core не должен дублировать серверную бизнес-логику.

---

# 9. Recommendation

Control Plane может рекомендовать:

```text
Node:
    DE-01

Protocol:
    WireGuard

Reason:
    lowest score

Expected RTT:
    43 ms
```

Концептуальная модель:

```go
type Recommendation struct {
    NodeID       string
    Protocol     string
    Reason       string
    Score        float64
    ExpiresAt    time.Time
}
```

Названия полей могут быть изменены при согласовании с API.

Важно:

> Client Core исполняет рекомендацию, но не должен воспроизводить серверный Smart Routing.

---

# 10. Smart Routing и Client Core

Серверный Smart Routing:

```text
                  Control Plane
                       │
             ┌─────────┴─────────┐
             │                   │
           Nodes             Protocols
             │                   │
             └─────────┬─────────┘
                       ▼
                Recommendation
```

Client Core:

```text
Recommendation
      │
      ▼
Connection Manager
      │
      ▼
Protocol Adapter
```

Разделение обязательно.

Control Plane отвечает на вопрос:

> Что сейчас лучше использовать?

Client Core отвечает на вопрос:

> Как это подключить на данном устройстве?

---

# 11. Protocol Adapter

Каждый VPN-протокол реализуется отдельным адаптером.

Базовая абстракция:

```go
type Protocol interface {
    Name() string

    Connect(
        ctx context.Context,
        config ConnectionConfig,
    ) error

    Disconnect(
        ctx context.Context,
    ) error

    Status() ProtocolStatus
}
```

Дополнительные методы допускаются только при необходимости.

---

# 12. Protocol Manager

Core не должен напрямую выбирать конкретную реализацию.

Используется менеджер:

```go
type ProtocolManager interface {
    Get(name string) (Protocol, error)
    Available() []Protocol
}
```

Например:

```text
Protocol Manager
       │
       ├── WireGuard
       ├── OpenVPN
       └── Xray
```

В будущем:

```text
       ├── VLESS
       ├── Amnezia
       └── ...
```

Добавление нового протокола не должно требовать переписывания Connection Manager.

---

# 13. Connection Config

Конфигурация подключения должна быть абстрактной:

```go
type ConnectionConfig struct {
    NodeID   string
    Protocol string
    Data     []byte
}
```

Core не должен знать внутреннюю структуру каждого VPN-протокола.

Например:

```text
WireGuard:
    Data → WireGuard configuration

OpenVPN:
    Data → OpenVPN configuration

Xray:
    Data → Xray configuration
```

Protocol Adapter самостоятельно интерпретирует данные.

---

# 14. Connection Manager

Connection Manager является главным orchestration-компонентом.

Он координирует:

```text
Control Plane
      │
      ▼
Recommendation
      │
      ▼
Connection Manager
      │
      ├── Protocol Manager
      │
      ├── Diagnostics
      │
      └── State
```

Connection Manager не должен содержать платформенный код.

---

# 15. Connection State

Состояние соединения должно быть конечным автоматом.

Минимальный набор:

```go
type ConnectionState string

const (
    StateDisconnected  ConnectionState = "disconnected"
    StateConnecting    ConnectionState = "connecting"
    StateConnected     ConnectionState = "connected"
    StateDegraded      ConnectionState = "degraded"
    StateSwitching     ConnectionState = "switching"
    StateDisconnecting ConnectionState = "disconnecting"
)
```

Не использовать набор независимых boolean-полей для основных состояний.

Недопустимо:

```text
connected=true
connecting=true
disconnecting=true
```

---

# 16. State Machine

Основные переходы:

```text
Disconnected
     │
     │ Connect
     ▼
Connecting
     │
     ├──────── failure ───────► Disconnected
     │
     ▼
Connected
     │
     ├── degradation ────────► Degraded
     │
     └── Disconnect ─────────► Disconnecting

Degraded
     │
     ├── recovered ──────────► Connected
     │
     └── switch ─────────────► Switching

Switching
     │
     ├── success ────────────► Connected
     │
     └── failure ────────────► Disconnected

Disconnecting
     │
     ▼
Disconnected
```

Дополнительные состояния допускаются, если они реально нужны.

---

# 17. Session

Каждое активное подключение должно иметь идентификатор session.

Пример:

```go
type Session struct {
    ID        string
    NodeID    string
    Protocol  string
    StartedAt time.Time
}
```

Session ID используется для:

- логирования;
- диагностики;
- connection reports;
- событий;
- correlation с Control Plane.

---

# 18. Auto Switch

Auto Switch является частью Client Core.

Он должен работать независимо от UI.

Типовой сценарий:

```text
Connected
    │
    ▼
Connection Monitor
    │
    ▼
Degraded / Lost
    │
    ▼
Recovery
    │
    ▼
Control Plane
    │
    ▼
Recommendation
    │
    ▼
Switch
```

Пример:

```text
DE-01 / WireGuard
        │
        │ degraded
        ▼
DE-01 / OpenVPN UDP
        │
        │ failed
        ▼
DE-02 / WireGuard
        │
        ▼
Connected
```

---

# 19. Anti-Flapping

Auto Switch не должен бесконечно переключать соединение:

```text
WG → OVPN → WG → OVPN → WG ...
```

Необходимо предусмотреть:

- cooldown;
- retry limit;
- failed-target suppression;
- минимальный интервал между switch;
- оценку результата предыдущего switch.

Конкретные значения должны быть конфигурируемыми и определяться после тестирования.

---

# 20. Recovery

Recovery отвечает за восстановление соединения.

При потере:

```text
Connection lost
       │
       ▼
Retry current protocol
       │
       ├── success → Connected
       │
       ▼
Request recommendation
       │
       ▼
Switch
```

Не каждый кратковременный packet loss должен немедленно вызывать смену сервера.

---

# 21. Diagnostics

Diagnostics является отдельным компонентом.

```go
type Diagnostics interface {
    Test(ctx context.Context) (DiagnosticResult, error)
}
```

Результат может содержать:

```text
Node
Protocol
RTT
Packet Loss
DNS
Internet
VPN
Timestamp
```

Например:

```go
type DiagnosticResult struct {
    NodeID       string
    Protocol     string
    RTT          time.Duration
    PacketLoss   float64
    DNSOK        bool
    InternetOK   bool
    VPNOK        bool
    TestedAt     time.Time
}
```

---

# 22. Verification

Успешный запуск protocol process сам по себе не означает успешное VPN-подключение.

После:

```text
Protocol.Connect()
```

должна выполняться verification.

Минимально:

```text
VPN interface exists
VPN is operational
Internet reachable
```

При необходимости:

```text
DNS
external IP
packet loss
RTT
```

Только после успешной verification Core переводит состояние в:

```text
Connected
```

---

# 23. Events

Core должен предоставлять event stream.

Пример:

```go
type Event struct {
    Type      EventType
    Timestamp time.Time
    SessionID string
    Data      any
}
```

Типы:

```text
StateChanged
ConnectionStarted
ConnectionEstablished
ConnectionLost
ConnectionDegraded
SwitchStarted
SwitchCompleted
SwitchFailed
DiagnosticCompleted
Error
```

GUI и CLI используют один источник событий.

---

# 24. UI не должен управлять State Machine

Недопустимо:

```text
GUI:
    если connection lost:
        вызвать OpenVPN
```

Допустимо:

```text
GUI:
    Connect()

Core:
    управляет состоянием
    выбирает protocol
    выполняет switch
```

GUI отображает состояние, но не владеет им.

---

# 25. Platform Layer

Core должен использовать платформенные абстракции.

Например:

```go
type Platform interface {
    VPN() VPNPlatform
    Network() NetworkPlatform
    SecureStore() SecureStore
    Process() ProcessManager
}
```

Конкретный состав интерфейса может уточняться при реализации.

---

# 26. Windows

Первой целевой платформой является Windows.

Ожидаемая архитектура:

```text
Wails GUI
    │
    ▼
Client Core
    │
    ▼
Platform interfaces
    │
    ▼
Windows implementation
```

Core не должен содержать:

```go
if runtime.GOOS == "windows"
```

по всей кодовой базе.

Windows-специфичные операции должны находиться в platform implementation.

---

# 27. Privileged Operations

VPN и системная маршрутизация могут требовать повышенных привилегий.

Архитектура должна позволять вынести privileged operations в Windows Service:

```text
singularity-vpn.exe
        │
        │ IPC
        ▼
Singularity VPN Service
        │
        ├── VPN
        ├── routes
        ├── firewall
        └── kill switch
```

При этом Client Core не должен зависеть от того, выполняется операция:

```text
directly
```

или:

```text
through service
```

Это обязанность Platform Layer.

Решение о необходимости Windows Service является отдельным этапом реализации и не должно ломать Client Core.

---

# 28. Secure Store

Секреты не должны храниться в обычном JSON/текстовом файле.

Core использует абстракцию:

```go
type SecureStore interface {
    Get(key string) ([]byte, error)
    Set(key string, value []byte) error
    Delete(key string) error
}
```

Windows implementation должна использовать подходящий механизм защищённого хранения Windows.

Core не должен быть привязан к конкретному механизму.

---

# 29. Local State

Клиенту требуется локальное состояние.

Оно может содержать:

```text
current profile
last selected node
last selected protocol
client preferences
last connection state
diagnostic cache
```

Секретные данные должны храниться отдельно через `SecureStore`.

Локальный state не должен становиться источником истины для серверных данных.

---

# 30. Server vs Client State

Control Plane является источником истины для:

- пользователя;
- аккаунта;
- тарифного плана;
- доступных nodes;
- доступных protocols;
- server-side recommendations;
- server-side connection configuration.

Client является источником истины для:

- текущей локальной session;
- текущего состояния VPN;
- локальных настроек;
- локальных диагностических данных.

---

# 31. Logging

Core должен использовать структурированное логирование.

Каждая существенная операция должна иметь:

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

Не следует писать секреты в logs.

Запрещено логировать:

- private keys;
- access tokens;
- passwords;
- полные VPN credentials;
- чувствительные configuration blobs.

---

# 32. Error Model

Ошибки должны позволять UI/CLI определить категорию проблемы.

Например:

```text
AuthenticationError
ControlPlaneUnavailable
RecommendationError
ConfigurationError
ProtocolUnavailable
ProtocolConnectionError
VerificationError
PlatformError
PermissionError
TimeoutError
```

Не следует заставлять GUI разбирать строки:

```text
"exit status 1"
```

для определения причины.

---

# 33. Timeouts

Сетевые операции должны иметь context/timeout.

Нельзя допускать зависания:

```text
Connect()
```

на неопределённое время.

Особенно это относится к:

- Control Plane;
- protocol startup;
- diagnostics;
- recovery;
- switch.

---

# 34. Concurrency

Core должен защищать состояние от конфликтующих операций.

Например:

```text
Connect()
    одновременно
Disconnect()
```

не должны приводить к неконсистентному состоянию.

Нужен механизм serialisation/coordination операций Connection Manager.

Например:

```text
Connect
Disconnect
Switch
```

могут быть последовательными операциями над одной session.

Диагностика, не изменяющая состояние, может выполняться параллельно при условии безопасности.

---

# 35. Idempotency

Основные операции должны быть максимально идемпотентными.

Например:

```text
Disconnect()
```

при уже отключённом клиенте не должен приводить к ошибке уровня application failure.

Повторный:

```text
Connect()
```

не должен создавать второе независимое VPN-соединение.

---

# 36. CLI и GUI

Оба интерфейса должны использовать Core:

```text
                  Client Core
                 /           \
                /             \
             Wails             CLI
```

CLI не должен иметь отдельную реализацию:

```text
WireGuard
OpenVPN
Auto Switch
Routing
```

GUI также не должен иметь её.

---

# 37. Wails

Wails является presentation layer.

Концептуально:

```go
type App struct {
    client client.Client
}
```

Wails bindings:

```text
Connect
Disconnect
Switch
Status
Test
```

Они вызывают Core.

Wails bindings не должны содержать VPN business logic.

---

# 38. Dependency Injection

Core должен получать зависимости извне.

Например:

```go
type Dependencies struct {
    ControlPlane ControlPlane
    Protocols    ProtocolManager
    Diagnostics  Diagnostics
    Platform     Platform
    State        StateStore
    Logger       Logger
}
```

Это позволяет:

- тестировать Core без Windows;
- тестировать без реального VPN;
- использовать mock Control Plane;
- запускать integration tests;
- использовать тот же Core из CLI.

---

# 39. Testability

Client Core должен быть тестируемым без реального VPN-подключения.

Минимальные mock implementations:

```text
MockControlPlane
MockProtocol
MockProtocolManager
MockPlatform
MockDiagnostics
MockStateStore
```

Тесты должны проверять:

- state transitions;
- connect;
- disconnect;
- switch;
- recovery;
- failed protocol;
- recommendation failure;
- verification failure;
- anti-flapping;
- concurrent operations.

---

# 40. Структура пакетов

Рекомендуемая структура:

```text
internal/client/
    core/
        client.go
        lifecycle.go

    api/
        control_plane.go
        models.go
        errors.go

    connection/
        manager.go
        session.go
        state.go

    protocols/
        protocol.go
        manager.go
        wireguard/
        openvpn/
        xray/

    routing/
        selector.go
        recommendation.go

    recovery/
        recovery.go
        auto_switch.go

    diagnostics/
        diagnostics.go
        models.go

    events/
        events.go
        bus.go

    state/
        store.go
        models.go

    platform/
        platform.go
        windows/

    security/
        secure_store.go
```

Фактическая структура может быть упрощена, если часть пакетов окажется избыточной.

Не следует создавать пакеты исключительно ради количества директорий.

---

# 41. Зависимости

Желаемое направление зависимостей:

```text
UI
 │
 ▼
Client Core
 │
 ├── interfaces
 │      │
 │      ├── Control Plane
 │      ├── Protocols
 │      ├── Platform
 │      ├── State
 │      └── Diagnostics
 │
 └── implementations
```

Business logic не должна зависеть напрямую от:

- Wails;
- Windows API;
- конкретного HTTP client;
- конкретного WireGuard executable;
- OpenVPN executable;
- filesystem layout.

---

# 42. Запрещённые архитектурные решения

Не делать:

```text
Wails → WireGuard
```

```text
CLI → WireGuard
```

```text
GUI → HTTP API → VPN executable
```

```text
Core → exec.Command("wireguard.exe")
```

```text
ConnectionManager → Windows API
```

```text
GUI → Smart Routing
```

```text
CLI → Auto Switch implementation
```

Все эти варианты нарушают разделение ответственности.

---

# 43. Connection Flow

Полный flow:

```text
User
 │
 ▼
GUI / CLI
 │
 ▼
Client Core
 │
 ├── validate request
 │
 ├── check current state
 │
 ├── Control Plane
 │      │
 │      ├── authenticate
 │      ├── recommendation
 │      └── connection config
 │
 ├── Protocol Manager
 │      │
 │      └── selected Protocol
 │
 ├── Platform
 │      │
 │      └── establish connection
 │
 ├── Diagnostics
 │      │
 │      └── verification
 │
 ├── State = Connected
 │
 └── Event
        │
        ▼
     GUI / CLI
```

---

# 44. Disconnect Flow

```text
Disconnect()
     │
     ▼
State = Disconnecting
     │
     ▼
Current Protocol.Disconnect()
     │
     ▼
Verify disconnected
     │
     ▼
State = Disconnected
     │
     ▼
Event
```

---

# 45. Switch Flow

```text
Switch()
   │
   ▼
State = Switching
   │
   ├── stop current protocol
   │
   ├── obtain target config
   │
   ├── start target protocol
   │
   ├── verify
   │
   └── State = Connected
```

При неудаче:

```text
Switch
  │
  ▼
target failed
  │
  ├── optionally restore previous connection
  │
  └── Recovery
```

Восстановление предыдущего соединения должно быть реализовано только после определения безопасной стратегии.

---

# 46. Auto Mode

Главный пользовательский сценарий:

```text
Connect
```

означает:

```text
получи оптимальную рекомендацию
→ подключись
→ проверь
→ поддерживай соединение
→ при необходимости переключись
```

Пользователь не обязан знать:

- какой node используется;
- какой protocol используется;
- почему произошёл switch.

GUI может показать эти данные как дополнительную информацию.

---

# 47. Manual Mode

Пользователь может выбрать:

```text
Node: DE-01
Protocol: WireGuard
```

Core должен выполнить запрос непосредственно с указанными параметрами.

Однако ручной выбор не должен отключать базовые механизмы:

- verification;
- diagnostics;
- connection state;
- safe disconnect.

Auto Switch в manual mode должен иметь явно определённое поведение.

Рекомендуемое поведение:

```text
Manual selection → Auto Switch disabled
```

пока пользователь явно не включит автоматическое переключение.

---

# 48. Future Protocols

Архитектура должна позволять добавить:

```text
WireGuard
OpenVPN UDP
OpenVPN TCP
Xray
VLESS
Amnezia
...
```

без изменения:

```text
GUI
CLI
Connection Manager
State Machine
Control Plane API
```

Новый protocol должен реализовать необходимый adapter contract.

---

# 49. Server-side Anti-DPI

Client Core не должен самостоятельно решать:

```text
какой DPI bypass применить
```

если это относится к серверной инфраструктуре.

Он получает от Control Plane уже доступный protocol/configuration.

Например:

```text
Control Plane:
    WireGuard unavailable
    OpenVPN TCP recommended

Client Core:
    execute OpenVPN TCP
```

---

# 50. Existing AntiZapret-VPN

Существующая серверная система:

```text
GubernievS/AntiZapret-VPN
```

не должна быть перенесена в Client Core.

Client Core работает с предоставленным сервером через стандартный protocol adapter.

Серверная интеграция и импорт существующих клиентов остаются задачей Control Plane/Server Deployment System.

---

# 51. MVP

Первый Client Core должен поддерживать:

```text
Control Plane API
       │
       ▼
Recommendation
       │
       ▼
WireGuard
       │
       ▼
Connect
       │
       ▼
Verify
       │
       ▼
Connected
```

Минимальный набор:

- Connect;
- Disconnect;
- Status;
- Control Plane;
- WireGuard adapter;
- state machine;
- events;
- diagnostics;
- local state;
- dependency injection.

---

# 52. Второй этап

Добавить:

- OpenVPN UDP;
- OpenVPN TCP;
- manual server selection;
- manual protocol selection;
- Switch;
- recovery;
- Auto Switch;
- anti-flapping;
- richer diagnostics.

---

# 53. Последующие этапы

Добавлять:

- Xray;
- VLESS;
- дополнительные anti-DPI protocols;
- Windows Service;
- kill switch;
- network change detection;
- automatic reconnect after sleep/resume;
- richer telemetry;
- per-network behavior;
- multiple profiles.

---

# 54. Условия готовности Client Core

Client Core считается архитектурно готовым для MVP, если:

1. GUI не содержит VPN business logic.
2. CLI не содержит отдельной VPN business logic.
3. Connection Manager не зависит от Windows API.
4. Protocols подключаются через adapter interface.
5. Control Plane доступен через interface.
6. Core можно запустить с mock dependencies.
7. State Machine имеет определённые переходы.
8. Connect/Disconnect являются безопасными при повторном вызове.
9. Ошибки типизированы по категориям.
10. Секреты не попадают в обычное локальное состояние и logs.
11. Новый protocol можно добавить без переписывания Core.
12. Wails можно заменить другим UI без переписывания Core.

---

# 55. Ключевой принцип проекта

Client Core должен оставаться **маленьким по ответственности, но центральным по значению**.

Он не должен превращаться в:

```text
God Object
```

который знает всё о Windows, WireGuard, HTTP, UI, firewall и filesystem.

Его задача — orchestration:

```text
что нужно сделать
        ↓
какую зависимость вызвать
        ↓
как изменить состояние
        ↓
как проверить результат
        ↓
что сообщить интерфейсу
```

Конкретные действия выполняют специализированные компоненты.

Итоговая модель:

```text
                 ┌───────────────┐
                 │     Wails     │
                 └───────┬───────┘
                         │
                 ┌───────▼───────┐
                 │  Client Core  │
                 │               │
                 │  Connection   │
                 │  Routing      │
                 │  Recovery     │
                 │  State        │
                 │  Diagnostics  │
                 └───┬─────┬─────┘
                     │     │
          ┌──────────┘     └──────────┐
          ▼                           ▼
 ┌─────────────────┐          ┌─────────────────┐
 │  Control Plane  │          │ Protocol Manager│
 └─────────────────┘          └───────┬─────────┘
                                      │
                         ┌────────────┼────────────┐
                         ▼            ▼            ▼
                    WireGuard      OpenVPN       Xray
                                     
                         │
                         ▼
                 ┌─────────────────┐
                 │ Platform Layer  │
                 └────────┬────────┘
                          │
                          ▼
                   Windows / OS
```

Это является базовой архитектурой клиентской части Singularity VPN и должно использоваться как основа для `WINDOWS_CLIENT.md`, CLI-интеграции и последующих клиентских платформ.