(function (root) {
  'use strict';
  if (!root.document) return;

  const $ = (id) => document.getElementById(id);
  const backend = () => (root.go && root.go.main && root.go.main.App) || (root.backend && root.backend());

  let env = {
    node_label: (id) => id ? "сервер " + id.slice(0, 8) : "сервер",
    protocol_label: (p) => p || "",
    get_expanded: () => false,
    get_reported_at: () => 0,
    get_games: () => [],
    get_servers_count: () => -1,
    load_servers: () => Promise.resolve(),
    render_parent: () => {}
  };

  let journal_entries = null;
  let journal_unsupported = false;
  let journal_error = false;
  let compact_journal = false;
  try { compact_journal = localStorage.getItem("compact.panel") === "journal"; } catch (e) {}
  let loading_journal = false;

  const reason_text = {
    least_loaded: "выбран наименее загруженный",
    chosen_node: "выбран вами",
    lowest_rtt: "самый быстрый канал"
  };
  const protocol_names = { wireguard: "WireGuard", amneziawg: "AmneziaWG", openvpn: "OpenVPN" };
  const level_text = { "": "Выкл", light: "Лёгкая", standard: "Средняя", high: "Высокая" };
  const tunnel_text = { vpn: "весь трафик", antizapret: "только заблокированные сайты", games: "только игры" };
  const event_text = {
    connection_lost: ["Связь с сервером потеряна", "warn"],
    connection_degraded: ["Проверяем соединение", "warn"],
    switch_started: ["Переключение", ""],
    switch_completed: ["Переключено", "ok"],
    switch_failed: ["Переключиться не удалось", "warn"]
  };

  function plural(n, one, few, many) {
    const m10 = n % 10, m100 = n % 100;
    if (m10 === 1 && m100 !== 11) return one;
    if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return few;
    return many;
  }

  function ago(iso) {
    const age = Math.max(0, Math.floor((Date.now() - Date.parse(iso)) / 1000));
    if (!(age >= 0)) return "";
    if (age < 5) return "только что";
    if (age < 60) return age + " с назад";
    const minutes = Math.floor(age / 60);
    if (minutes < 60) return minutes + " " + plural(minutes, "минуту", "минуты", "минут") + " назад";
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return hours + " " + plural(hours, "час", "часа", "часов") + " назад";
    const days = Math.floor(hours / 24);
    return days + " " + plural(days, "день", "дня", "дней") + " назад";
  }

  function full_date(iso) {
    const t = Date.parse(iso);
    return Number.isFinite(t) ? new Date(t).toLocaleString("ru-RU") : "";
  }

  function bytes_text(n) {
    n = Number(n);
    if (!isFinite(n)) return "";
    if (n < 1024) return n + " Б";
    return (n / 1024).toFixed(n < 10240 ? 1 : 0).replace(".", ",") + " КБ";
  }

  function transport_text(report) {
    if (!report) return "";
    const words = [report.dial_failed + " " + plural(report.dial_failed, "сброс", "сброса", "сбросов")];
    if (report.tls_failed) words.push(report.tls_failed + " " + plural(report.tls_failed, "ошибка TLS", "ошибки TLS", "ошибок TLS"));
    if (report.timeout) words.push(report.timeout + " " + plural(report.timeout, "таймаут", "таймаута", "таймаутов"));
    if (report.bytes) words.push(bytes_text(report.bytes));
    return words.join(", ");
  }

  function list_of(value) {
    if (!value) return [];
    if (Array.isArray(value)) return value;
    if (typeof value === "string") {
      try {
        const parsed = JSON.parse(value);
        if (Array.isArray(parsed)) return parsed;
      } catch (e) {}
      return value.split(/[\s,]+/).filter(Boolean);
    }
    return [];
  }

  function journal_line(e) {
    const a = e.attrs || {};
    const p = env.protocol_label(a.protocol);
    switch (e.message) {
      case "service starting": return ["Служба запущена", ""];
      case "service stopped": return ["Служба остановлена", ""];
      case "service stopped with an error": return ["Служба остановлена с ошибкой", "warn", a.error];
      case "service ended without stopping": return ["Служба завершилась, не остановившись: сбой или выключение компьютера", "warn"];
      case "connect requested": return [a.node_id ? "Подключение к " + env.node_label(a.node_id) : "Подключение, сервер выбирается автоматически", ""];
      case "disconnect requested": return ["Отключение по вашей команде", ""];
      case "the control plane moved; its address came from a signed manifest":
        return ["Сервер управления переехал — новый адрес взят из подписанного обновления", ""];
      case "control plane recommendation": {
        const why = reason_text[a.reason] ? ", " + reason_text[a.reason] : "";
        return ["Сервер: " + env.node_label(a.node_id) + why + "; рекомендован " + p, ""];
      }
      case "using an offered alternative this build speaks":
        return ["Пробуем " + p + " вместо рекомендованного", ""];
      case "leaving aside what this network reported or keeps losing":
        return ["Протоколы, которые в этой сети отваливались или о которых вы сообщали, пробуются последними", ""];
      case "the connection keeps dropping; moving to another protocol": {
        const to = [a.next_node_id && a.next_node_id !== a.node_id ? env.node_label(a.next_node_id) : "", env.protocol_label(a.next_protocol)].filter(Boolean).join(", ");
        return [p + ": связь то и дело пропадает — переходим на " + (to || "другой протокол"), "warn"];
      }
      case "the connection keeps dropping; there is nothing else to move to":
        return [p + ": связь то и дело пропадает, а перейти не на что", "warn", a.reason];
      case "the connection keeps dropping after a move for drops; staying":
        return [p + " пропадает так же часто — дело не в протоколе. Следующая смена не раньше чем через час", "warn"];
      case "the connection moved to did not hold; going back":
        return [p + " не удержался — возвращаемся на " + env.protocol_label(a.back_protocol), "warn"];
      case "the connection failed": return ["Не подключено — нажмите «Подключиться»", "warn", a.error];
      case "openvpn event": return root.health_text ? root.health_text.openvpn(a.event) : ["OpenVPN: " + a.event, ""];
      case "using the offered protocol that worked last on this node":
        return ["Пробуем " + p + ": в прошлый раз на этом сервере сработал он", ""];
      case "verification failed":
        return [p + " поднят, но трафик не идёт: " + transport_text(a.transport) + ". Пробуем дальше", "warn", a.transport];
      case "reconnected at start: the machine was left connected": return ["Подключение восстановлено после запуска", "ok"];
      case "retry on the same node failed to connect": return ["Переподключиться к тому же серверу не удалось", "warn", a.err];
      case "retry on the same node failed verification": return ["Переподключились к тому же серверу, но трафик не идёт", "warn", a.err];
      case "network changed; moving the endpoint route failed": return ["Сеть изменилась, а маршрут к серверу перенести не удалось", "warn", a.error];
      case "strategy set": {
        const parts = [a.node_id ? "сервер закреплён: " + env.node_label(a.node_id) : "сервер — автоматически"];
        if (a.protocol) parts.push("протокол: " + (protocol_names[a.protocol] || a.protocol));
        if (a.route === "true") parts.push("самый быстрый маршрут");
        if (a.bypass === "true") parts.push("обход блокировок");
        if (a.anonymity) parts.push("анонимность: " + (level_text[a.anonymity] || a.anonymity).toLowerCase());
        if (a.country) {
          const c = root.servers_ui ? root.servers_ui.country_of(a.country) : { name: a.country };
          parts.push("страна: " + (c.name || a.country));
        }
        return ["Настройки: " + parts.join(", "), ""];
      }
      case "games set": {
        const ids = list_of(a.games);
        const games = env.get_games() || [];
        const names = ids.map((id) => ((games || []).find((g) => g.id === id) || {}).name || id);
        return [names.length ? "Через сервер идут игры: " + names.join(", ") : "Игры выключены", ""];
      }
      case "tunnel mode set": return ["Режим: " + (a.mode === "antizapret" ? "Антизапрет" : "VPN"), ""];
      case "game router started": return ["Игры отделены от остального трафика", "ok"];
      case "game router stopped": return ["Разделение игр остановлено", ""];
      case "game router: capture stopped; games go direct until the next connect":
        return ["Перехват игр остановился — до следующего подключения игры идут напрямую", "warn", a.error];
      case "event": {
        if (a.kind === "connection_degraded") return [root.health_text ? root.health_text.degraded(a) : "Связь ухудшилась", "warn"];
        if (a.kind === "connection_blip") return ["Короткий провал · " + (root.health_text ? root.health_text.degraded(a) : "сбой"), "warn"];
        if (a.kind === "switch_started" && a.to_protocol) {
          const to = [a.to_node_id && a.to_node_id !== a.node_id ? env.node_label(a.to_node_id) : "", env.protocol_label(a.to_protocol)].filter(Boolean).join(", ");
          return ["Переключение на " + to, ""];
        }
        const t = event_text[a.kind];
        if (!t) return [a.kind, ""];
        if (a.node_id && (a.kind === "connection_established" || a.kind === "switch_completed")) {
          const how = [env.node_label(a.node_id), p, tunnel_text[a.tunnel]].filter(Boolean).join(", ");
          return [t[0] + ": " + how, t[1]];
        }
        return [t[0], t[1], a.error];
      }
    }
    const detail = Object.keys(a).map((k) => k + "=" + a[k]).join(" ");
    return [e.message, e.level === "info" ? "" : "warn", detail];
  }

  const is_blip = (e) => e.message === "event" && !!e.attrs && e.attrs.kind === "connection_blip";
  const clock_time = (at) => new Date(at).toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" });
  function span_text(ms) {
    const minutes = Math.max(1, Math.round(ms / 60000));
    return minutes < 60 ? minutes + " мин" : Math.round(minutes / 60) + " ч";
  }

  async function load_journal() {
    if (loading_journal || journal_unsupported) return;
    const app = backend();
    if (!app || typeof app.Journal !== "function") { journal_unsupported = true; return; }
    loading_journal = true;
    render_journal();
    let answer = null;
    try { answer = await app.Journal(); } catch (e) { answer = null; } finally { loading_journal = false; }
    if (answer && answer.error && answer.error.code === "unknown_command") { journal_unsupported = true; render_journal(); return; }
    journal_error = !answer || !!answer.error;
    if (answer && !answer.error) {
      journal_entries = answer.entries || [];
      if (env.get_servers_count() < 0 && journal_entries.some((e) => e.attrs && e.attrs.node_id)) {
        env.load_servers().then(render_journal);
      }
    }
    render_journal();
  }

  function render_journal() {
    const box = $("journal");
    if (!box) return;
    const expanded = env.get_expanded();
    const show = expanded ? !!(journal_entries && journal_entries.length) : compact_journal;
    const parent = expanded ? document.querySelector(".side") : $("games");
    if (parent && box.parentElement !== parent) parent.append(box);
    const compactBtn = $("compact-journal");
    if (compactBtn) {
      compactBtn.hidden = expanded;
      compactBtn.setAttribute("aria-expanded", String(!expanded && compact_journal));
    }
    const titleBtn = $("journal-title");
    if (titleBtn) titleBtn.disabled = expanded;
    const noteEl = $("journal-note");
    if (noteEl) {
      noteEl.textContent = journal_unsupported || journal_error ? "Журнал недоступен" : journal_entries === null && loading_journal ? "Загрузка…" : !journal_entries || !journal_entries.length ? "Записей пока нет" : "";
      noteEl.hidden = !noteEl.textContent;
    }
    const retryBtn = $("journal-retry");
    if (retryBtn) retryBtn.hidden = !journal_error || journal_unsupported;
    box.hidden = !show;
    if (!show) return;
    const entries = journal_entries || [];
    const reportBtn = $("journal-report");
    if (reportBtn) {
      reportBtn.hidden = !entries.some((e) => Date.parse(e.at) > env.get_reported_at() && journal_line(e)[1] === "warn");
    }
    const list = $("journal-list");
    if (!list) return;
    const key = entries.length + ":" + (entries[entries.length - 1] || {}).at + ":" + env.get_servers_count();
    if (list.dataset.key === key) return;
    list.dataset.key = key;
    list.textContent = "";
    for (let i = entries.length - 1; i >= 0; i--) {
      const e = entries[i];
      let [text, tone, tip] = journal_line(e);
      let run = 1;
      while (is_blip(e) && i - run >= 0 && is_blip(entries[i - run])) run++;
      if (run > 1) {
        const lines = [];
        for (let k = i; k > i - run; k--) lines.push(clock_time(entries[k].at) + " — " + journal_line(entries[k])[0].replace(/^Короткий провал · /, ""));
        text = "Короткие провалы ×" + run + " за " + span_text(Date.parse(e.at) - Date.parse(entries[i - run + 1].at));
        tip = lines.slice(0, 12).join("\n") + (lines.length > 12 ? "\n…и ещё " + (lines.length - 12) : "");
        i -= run - 1;
      }
      const li = document.createElement("li");
      if (tone) li.className = tone;
      const when = document.createElement("span");
      when.className = "when";
      when.dataset.time = e.at;
      when.dataset.tip = full_date(e.at);
      when.textContent = ago(e.at);
      const what = document.createElement("span");
      what.className = "what";
      what.textContent = text;
      if (tip) what.dataset.tip = tip;
      li.append(when, what);
      list.append(li);
    }
  }

  const journal_min_px = 64;
  let last_window_height = 0;
  if ($("window")) {
    new ResizeObserver(() => {
      const height = $("window").clientHeight;
      const shrank = height < last_window_height;
      last_window_height = height;
      if (!shrank || env.get_expanded() || !compact_journal || $("journal").hidden || $("window").classList.contains("measuring")) return;
      if ($("journal-list").getBoundingClientRect().height < journal_min_px) toggle_journal();
    }).observe($("window"));
  }

  function toggle_journal() {
    const old = compact_journal ? $("journal") : $("game-cards");
    const parent = $("games");
    if (!old || !parent) return;
    const rect = old.getBoundingClientRect(), base = parent.getBoundingClientRect();
    const snapshot = old.cloneNode(true);
    snapshot.removeAttribute("id");
    snapshot.querySelectorAll("[id]").forEach(el => el.removeAttribute("id"));
    snapshot.setAttribute("aria-hidden", "true");
    snapshot.inert = true;
    snapshot.style.cssText = `position:absolute;left:${rect.left-base.left}px;top:${rect.top-base.top}px;width:${rect.width}px;height:${rect.height}px;pointer-events:none;z-index:1;`;
    compact_journal = !compact_journal;
    try { localStorage.setItem("compact.panel", compact_journal ? "journal" : "games"); } catch (e) {}
    env.render_parent();
    const next = compact_journal ? $("journal") : $("game-cards");
    if (next && !window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      parent.append(snapshot);
      const duration = 100;
      next.animate([{ opacity: 0 }, { opacity: 1 }], { duration, easing: "ease-out" });
      snapshot.animate([{ opacity: 1 }, { opacity: 0 }], { duration, easing: "ease-out" }).finished.then(() => snapshot.remove(), () => snapshot.remove());
    }
    if (!compact_journal && $("compact-journal")) $("compact-journal").focus();
  }

  function init(options) {
    if (options) Object.assign(env, options);
  }

  const api = {
    init,
    load: load_journal,
    render: render_journal,
    toggle: toggle_journal,
    line: journal_line,
    entries: () => journal_entries,
    is_compact: () => compact_journal,
    set_compact: (v) => {
      compact_journal = !!v;
      try { localStorage.setItem("compact.panel", compact_journal ? "journal" : "games"); } catch (e) {}
    }
  };

  root.journal_ui = api;
  if (typeof module !== 'undefined') module.exports = api;
})(typeof window !== 'undefined' ? window : globalThis);
