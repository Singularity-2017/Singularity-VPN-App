# CLIENT_AUTO_SWITCH.md — Executable Client Auto-Switch Logic

## 0. Назначение

Определяет:

* как клиент переключает протоколы
* когда переключает
* как избегает циклов и флаппинга
* как взаимодействует с API

❗ Это алгоритм поведения клиента, не описание.

---

## 1. Модель клиента

Клиент хранит:

```text id="m1"
current_protocol
current_node
connection_state
last_success_protocol
fail_history
cooldowns
```

---

## 2. Состояния соединения

```text id="states"
DISCONNECTED
CONNECTING
CONNECTED
DEGRADED
FAILED
```

---

## 3. Старт подключения

---

## 3.1 Получение конфигурации

```text id="s1"
call /vpn/auto
```

---

## 3.2 Ответ

```text id="s2"
protocol
node
config
alternatives[]
```

---

## 3.3 Действие

```text id="s3"
current_protocol = protocol
connect()
```

---

## 4. Условия успешного подключения

---

```text id="success"
handshake_success == true
AND internet_access == true
```

---

## 5. Fail Detection

---

## 5.1 HARD_FAIL

```text id="hf"
handshake_failed
OR timeout > 5s
```

---

## 5.2 SOFT_FAIL

```text id="sf"
packet_loss > 5%
OR latency > 2x baseline
OR frequent disconnects
```

---

## 6. Реакция на HARD_FAIL

---

```text id="hfr"
add current_protocol to fail_history

disconnect()

switch_to_next_protocol()
```

---

## 7. Реакция на SOFT_FAIL

---

```text id="sfr"
state = DEGRADED

start degradation_timer (10–20 сек)

if condition persists:
    switch_to_next_protocol()
```

`frequent disconnects` — короткие провалы, каждый из которых проходит раньше
`degradation_timer`: плохой замер, следующий хороший. Такая связь не остаётся
`DEGRADED` достаточно долго и без отдельного правила держится часами.

```text id="sfr-drops"
5 провалов за 15 минут, режим auto, не идёт матч:
    switch_to_next_protocol()   -- что бы ни делал человек на компьютере
следующее такое переключение -- не раньше чем через час:
    новый протокол проваливается так же часто -> дело не в протоколе
не поднялось ничего другого -> назад на прежний, вне лимита попыток:
    он проваливался, но работал
перейти не на что -> переключения не было, час не отсчитывается
смена сети обнуляет и счёт, и час
```

---

## 8. Выбор следующего протокола

---

## 8.1 Источник

```text id="n1"
alternatives[] из API
```

---

## 8.2 Фильтрация

```text id="n2"
исключить протоколы из fail_history
исключить протоколы в cooldown
в конец очереди -- что эта сеть в этом режиме держит против протокола:
    жалобу (problem report) или
    failures >= 2 и failures > attempts / 2   -- например, WireGuard, который
                                                 обрывают через секунды
```

---

## 8.3 Выбор

```text id="n3"
next = first available
```

---

## 8.4 Если нет вариантов

```text id="n4"
reset fail_history
```

---

## 9. Переключение

---

```text id="sw1"
disconnect current
apply new config
connect
```

---

## 10. Ограничение попыток

---

```text id="limit1"
max_switch_attempts = 3
```

---

## 10.1 Если превышено

```text id="limit2"
state = FAILED
show error
```

---

## 11. Cooldown система

---

## 11.1 После fail

```text id="cd1"
protocol cooldown = 5–10 минут
```

---

## 11.2 Хранение

```text id="cd2"
cooldowns[protocol] = timestamp
```

---

## 12. Runtime Monitoring

---

## 12.1 Интервал

```text id="rt1"
каждые 5–10 секунд
```

---

## 12.2 Проверки

```text id="rt2"
latency
packet_loss
connection_alive
```

---

## 13. Auto-Recovery

---

## 13.1 Если DISCONNECTED

```text id="ar1"
retry last_success_protocol
```

---

## 13.2 Если fail

```text id="ar2"
call /vpn/auto
```

---

## 14. Anti-Flapping защита

---

## 14.1 Проблема

```text id="af1"
частые переключения туда-сюда
```

---

## 14.2 Решение

```text id="af2"
min_stable_time = 30–60 сек
```

---

## 14.3 Правило

```text id="af3"
не переключаться чаще чем min_stable_time
```

---

## 15. Background Re-evaluation

---

## 15.1 Интервал

```text id="bg1"
каждые 2–5 минут
```

---

## 15.2 Действие

```text id="bg2"
периодически переоценивать доступные протоколы

если есть вариант лучше:
    предложить switch
```

---

## 16. UX поведение

---

## 16.1 Автоматический режим

```text id="ux1"
silent switch (без уведомления)
```

---

## 16.2 Критическая ошибка

```text id="ux2"
"Не удалось подключиться. Попробуйте вручную."
```

---

## 17. Логирование

---

Каждое переключение:

```text id="log1"
timestamp
old_protocol
new_protocol
reason
result
```

---

## 18. Definition of Done

---

Система считается реализованной если:

```text id="done1"
1. клиент подключается ≤ 2 попыток
2. fallback работает автоматически
3. нет циклических переключений
4. пользователь не участвует
```

---

## 19. Финальное правило

---

```text id="rule1"
Клиент не думает.
Клиент переключает.
```

---

END OF FILE