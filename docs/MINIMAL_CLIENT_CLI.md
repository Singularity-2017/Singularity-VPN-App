# MINIMAL_CLIENT_CLI.md — Singularity VPN Minimal CLI Client

> **Устарел с 2026-09-15.** Заменён документами `CLIENT_CORE.md`,
> `WINDOWS_CLIENT.md` и
> `superpowers/specs/2026-09-15-singularity-vpn-app-design.md`.
>
> Расходится с ними в главном. Здесь записано, что клиент не реализует VPN, а
> управляет системными WireGuard и OpenVPN, которые пользователь установил
> сам. Принятое решение — обратное: туннель встроен в приложение, `wintun.dll`
> и библиотека `wireguard-go` едут внутри установщика. Иначе первый запуск это
> инструкция на три шага, а не одна кнопка.
>
> Документ оставлен, а не удалён: разделы 3 и 5–9 описывают набор команд CLI и
> поведение при отказе, а CLI по-прежнему планируется — он будет работать
> поверх того же Client Core, что и окно. Всё, что здесь сказано про запуск
> `wg-quick` и `openvpn` как внешних программ, читать как историю.

## 0. Назначение

CLI-клиент предназначен для:

* получения VPN-конфига
* подключения к VPN
* переключения протоколов
* диагностики соединения

❗ CLI НЕ реализует VPN. Он управляет уже существующими системными клиентами (WireGuard / OpenVPN).

---

## 1. Технологический стек

```text id="t1"
language: Go
runtime: CLI binary
dependencies:
- wireguard-tools (wg)
- openvpn
- curl
```

---

## 2. Общая архитектура

```text id="a1"
CLI → Control Plane API → config → system VPN client
```

CLI НЕ держит туннель сам.

---

## 3. Команды CLI

---

## 3.1 login (опционально MVP)

```bash id="c1"
vpn login <token>
```

Сохраняет:

```text id="c2"
API token → local config
```

---

## 3.2 connect

```bash id="c3"
vpn connect
```

Действие:

```text id="c4"
1. запрос /vpn/auto
2. получение config
3. запуск VPN клиента
```

---

## 3.3 status

```bash id="c5"
vpn status
```

Вывод:

```text id="c6"
connected / disconnected
protocol
node
latency (если есть)
```

---

## 3.4 switch

```bash id="c7"
vpn switch
```

Действие:

```text id="c8"
1. получить alternatives
2. остановить текущий VPN
3. запустить новый
```

---

## 3.5 disconnect

```bash id="c9"
vpn disconnect
```

---

## 3.6 test

```bash id="c10"
vpn test
```

Запускает:

```text id="c11"
SELF_TEST_SYSTEM (remote API)
```

---

## 4. Подключение к Control Plane

---

## 4.1 Базовый endpoint

```text id="e1"
POST /vpn/auto
POST /vpn/connect
POST /vpn/test
```

---

## 4.2 Ответ API

```json id="e2"
{
  "node": "NODE-1",
  "protocol": "wireguard",
  "config": "...",
  "alternatives": ["openvpn_tcp", "vless"]
}
```

---

## 5. Auto-switch логика (CLI сторона)

---

## 5.1 Fail detection

```text id="f1"
if ping fails OR tunnel down:
    trigger switch()
```

---

## 5.2 Switch алгоритм

```text id="f2"
1. disconnect current
2. request alternatives
3. try next protocol
```

---

## 5.3 Ограничение

```text id="f3"
max_switch_attempts = 3
```

---

## 6. Локальное состояние

---

CLI хранит:

```text id="s1"
current_protocol
current_node
last_success
fail_count
```

---

## 7. Запуск системных клиентов

---

## 7.1 WireGuard

```bash id="w1"
wg-quick up <config>
```

---

## 7.2 OpenVPN

```bash id="o1"
openvpn --config <file>
```

---

## 8. Health check

---

## 8.1 Проверка интернета

```text id="h1"
curl https://example.com
```

---

## 8.2 Ping fallback

```text id="h2"
ping 1.1.1.1
```

---

## 9. Error handling

---

## 9.1 VPN не поднялся

```text id="e3"
retry with next protocol
```

---

## 9.2 Все протоколы fail

```text id="e4"
exit with error:
NO_AVAILABLE_PROTOCOL
```

---

## 10. UX поведения

---

## 10.1 Успешное подключение

```text id="u1"
Connected to NODE-1 via WireGuard
Latency: 34ms
```

---

## 10.2 Переключение

```text id="u2"
Switching to OpenVPN TCP...
```

---

## 11. Режимы работы

---

## 11.1 Single node

```text id="m1"
switch only protocol
```

---

## 11.2 Multi-node (future)

```text id="m2"
switch node + protocol
```

---

## 12. Логирование

---

```text id="l1"
timestamp
command
result
protocol
node
error
```

---

## 13. Безопасность (минимум)

---

CLI НЕ:

```text id="sec1"
- хранит приватные ключи постоянно
- логирует конфиги
```

---

## 14. Definition of Done

---

CLI считается готовым если:

```text id="d1"
1. может подключиться к VPN
2. может переключить протокол
3. может отключиться
4. работает без GUI
```

---

## 15. Финальное правило

---

```text id="r1"
CLI не управляет VPN.
CLI управляет выбором VPN.
```

---

END OF FILE
