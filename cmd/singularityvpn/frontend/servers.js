(function (root) {
  'use strict';
  if (!root.document) return;

  const $ = (id) => document.getElementById(id);
  const backend = () => (root.go && root.go.main && root.go.main.App) || (root.backend && root.backend());
  const flags = root.flags;

  let env = {
    get_strategy: () => ({}),
    get_status: () => ({}),
    change: () => {},
    open_pop: () => {},
    close_pop: () => {},
    close_pops: () => {}
  };

  let servers = null;
  const open_groups = new Set();
  let list_request = 0;
  const pings = {};
  let ping_timer = 0;
  let ping_pending = false;
  const ping_keep_ms = 3000;
  const ping_tip = "Ping с этого компьютера напрямую, мимо туннеля; пока меню открыто, обновляется раз в 5 секунд.";

  let default_pinned = false;
  try { default_pinned = localStorage.getItem("server.default_pinned") === "1"; } catch (e) {}

  const region_names = make_region_names("ru");
  const english_names = make_region_names("en");

  function make_region_names(locale) {
    try { return new Intl.DisplayNames([locale], { type: "region" }); } catch (e) { return null; }
  }
  function region_name(names, code) {
    try { return (names && names.of(code)) || ""; } catch (e) { return ""; }
  }

  function country_of(raw) {
    const code = flags ? flags.country_code(raw) : "";
    return { code, name: code ? (region_name(region_names, code) || code) : String(raw || "") };
  }

  function short_name(name, raw_country, city) {
    if (!name) return city || "";
    const code = flags ? flags.country_code(raw_country) : "";
    const spellings = [raw_country, code, region_name(region_names, code), region_name(english_names, code)]
      .concat(flags && flags.names_for ? flags.names_for(code) : [])
      .filter(Boolean)
      .map((w) => String(w).toLowerCase())
      .sort((a, b) => b.length - a.length);
    const lower = name.toLowerCase();
    for (const w of spellings) {
      if (lower === w) return city || name;
      if (lower.startsWith(w) && /[\s\-_·.,:]/.test(name.charAt(w.length))) {
        const rest = name.slice(w.length).replace(/^[\s\-_·.,:]+/, "");
        if (rest) return rest;
      }
    }
    return name;
  }

  function flag_into(el, code) {
    el.textContent = "";
    if (!flags) return;
    const svg = flags.flag_svg(code);
    if (svg) { el.innerHTML = svg; return; }
    if (!code) return;
    const box = document.createElement("span");
    box.className = "flag code";
    box.textContent = code;
    el.append(box);
  }

  function server_by_id(id) {
    return (servers || []).find((s) => s.node_id === id) || null;
  }

  function node_label(id) {
    const s = server_by_id(id);
    if (!s) return id ? "сервер " + id.slice(0, 8) : "сервер";
    const c = country_of(s.country);
    const short = c.name ? short_name(s.name, s.country, s.city) : "";
    return c.name ? c.name + (short ? " · " + short : "") : s.name || "сервер " + s.node_id.slice(0, 8);
  }

  function ms(value) {
    return typeof value === "number" && Number.isFinite(value) ? Math.round(value) + " мс" : "";
  }

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

  function group_servers() {
    const groups = new Map();
    for (const server of servers || []) {
      const c = country_of(server.country);
      const key = c.code || c.name;
      if (!groups.has(key)) groups.set(key, { key, code: c.code, name: c.name, servers: [] });
      groups.get(key).servers.push(server);
    }
    const list = Array.from(groups.values());
    for (const g of list) {
      g.servers.sort((a, b) => label_of(a).localeCompare(label_of(b), "ru"));
    }
    return list.sort((a, b) => {
      if (!a.name !== !b.name) return a.name ? -1 : 1;
      return a.name.localeCompare(b.name, "ru");
    });
  }

  function label_of(server) {
    const c = country_of(server.country);
    return (c.name ? short_name(server.name, server.country, server.city) : server.name) || server.city || server.node_id;
  }

  function group_ping(group) {
    let best = null;
    for (const server of group.servers) {
      const p = server.available ? pings[server.node_id] : null;
      if (!p) continue;
      if (best === null || p.at > best.at + ping_keep_ms || (Math.abs(p.at - best.at) <= ping_keep_ms && p.rtt < best.rtt)) best = p;
    }
    return best;
  }

  function ping_into(el, p) {
    if (!p) { el.textContent = ""; el.dataset.tip = ping_tip; return; }
    const at = new Date(p.at).toISOString();
    const fresh = Date.now() - p.at < ping_keep_ms;
    el.textContent = ms(p.rtt) + (fresh ? "" : " · " + ago(at));
    el.classList.toggle("stale", !fresh);
    el.dataset.tip = ping_tip + (fresh ? "" : " Замер: " + full_date(at) + ".");
  }

  function ping_span(kind, key) {
    const span = document.createElement("span");
    span.className = "ping num";
    span.dataset[kind] = key;
    span.dataset.tip = ping_tip;
    return span;
  }

  function load_pings() {
    try {
      const saved = JSON.parse(localStorage.getItem("pings") || "{}");
      for (const id of Object.keys(saved)) {
        const p = saved[id];
        if (p && typeof p.rtt === "number" && typeof p.at === "number") pings[id] = { rtt: p.rtt, at: p.at };
      }
    } catch (e) { /* a convenience */ }
  }

  function save_pings() {
    try { localStorage.setItem("pings", JSON.stringify(pings)); } catch (e) { /* a convenience */ }
  }

  function live_node() {
    const st = env.get_status();
    return st && st.session_id ? st.node_id || "" : "";
  }

  function auto_option(what, selected, tip, on_pick) {
    const b = option(what + " ", selected, on_pick);
    const badge = document.createElement("span");
    badge.className = "chip-badge auto";
    badge.textContent = "Авто";
    b.querySelector(".opt-name").append(badge);
    b.dataset.tip = tip;
    return b;
  }

  function option(text, selected, on_pick) {
    const b = document.createElement("button");
    b.className = "opt line";
    b.setAttribute("role", "option");
    b.setAttribute("aria-selected", selected ? "true" : "false");
    const name = document.createElement("span");
    name.className = "opt-name";
    name.textContent = text;
    b.append(name);
    b.addEventListener("click", on_pick);
    return b;
  }

  function render_server_policy() {
    const s = env.get_strategy() || {};
    const pinned = s.node_id ? !!s.pinned : default_pinned;
    const policyPop = $("server-policy");
    if (!policyPop) return;
    for (const b of policyPop.querySelectorAll("[data-server-policy]")) {
      b.setAttribute("aria-selected", (b.dataset.serverPolicy === "pinned") === pinned ? "true" : "false");
    }
  }

  function render_server_menu() {
    const list = $("server-list");
    if (servers === null || !list) return;
    const scroll = $("servers").scrollTop;
    list.textContent = "";
    const s = env.get_strategy() || {};
    const auto = !s.node_id;
    const strategy_code = flags ? flags.country_code(s.country) : "";
    const set_server = (node_id, ctrl) => {
      let want_pinned = ctrl ? !default_pinned : default_pinned;
      if (s.node_id === node_id) {
        if (ctrl) want_pinned = !s.pinned;
        else if (s.pinned) want_pinned = false;
      }
      env.change({ node_id, pinned: !!node_id && want_pinned });
    };
    const live = live_node();

    list.append(auto_option("Страна", auto && !strategy_code, "Страна и сервер выбираются автоматически.", () => env.change({ country: "", node_id: "" })));

    for (const group of group_servers()) {
      const open = open_groups.has(group.key);
      const head = document.createElement("button");
      head.className = "group";
      head.setAttribute("aria-expanded", open ? "true" : "false");
      head.classList.toggle("picked", auto && !!group.code && group.code === strategy_code);
      head.classList.toggle("live", !!live && group.servers.some((x) => x.node_id === live));
      head.innerHTML = '<svg class="chev" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m9 6 6 6-6 6"/></svg>';
      const flag = document.createElement("span");
      flag.className = "group-flag";
      flag_into(flag, group.code);
      const name = document.createElement("span");
      name.className = "group-name";
      name.textContent = group.name || "Без страны";
      const count = document.createElement("span");
      count.className = "group-count num";
      count.textContent = String(group.servers.length);
      head.append(flag, name, count, ping_span("group", group.key));
      head.addEventListener("click", () => {
        if (open_groups.has(group.key)) open_groups.delete(group.key);
        else open_groups.add(group.key);
        render_server_menu();
      });
      list.append(head);
      if (!open) continue;

      const rows = [];
      if (group.code) {
        rows.push(auto_option("Сервер", auto && group.code === strategy_code, "Сервер в этой стране выбирается автоматически.", () => env.change({ country: group.code, node_id: "" })));
      }
      for (const server of group.servers) {
        const selected = s.node_id === server.node_id;
        const b = option(label_of(server), selected, (event) => set_server(server.node_id, !!(event && (event.ctrlKey || event.metaKey))));
        if (selected) {
          b.dataset.tip = s.pinned
            ? "Сервер закреплён. Обычный щелчок переключит его на предпочтительный режим."
            : "Предпочитаемый сервер. Щёлкните с Ctrl, чтобы закрепить его.";
        } else {
          b.dataset.tip = default_pinned
            ? "Щёлкните, чтобы закрепить сервер. Щёлкните с Ctrl, чтобы сделать его предпочтительным."
            : "Щёлкните, чтобы предпочесть сервер. Щёлкните с Ctrl, чтобы закрепить его.";
        }
        if (server.node_id === live) {
          b.classList.add("live");
          b.dataset.tip = selected ? (s.pinned
            ? "Закреплённый сервер; подключено. Обычный щелчок сделает его предпочтительным."
            : "Предпочитаемый сервер; подключено. Щёлкните с Ctrl, чтобы закрепить его.")
            : (default_pinned
                ? "Подключено к этому серверу. Щёлкните, чтобы закрепить его; с Ctrl — чтобы предпочесть."
                : "Подключено к этому серверу. Щёлкните, чтобы предпочесть его; с Ctrl — чтобы закрепить.");
          const dot = document.createElement("span");
          dot.className = "live-dot";
          b.append(dot);
        }
        b.disabled = !server.available;
        if (server.available) {
          b.append(ping_span("node", server.node_id));
        } else {
          const off = document.createElement("span");
          off.className = "ping";
          off.textContent = "недоступен";
          b.append(off);
        }
        rows.push(b);
      }
      const live_at = rows.findIndex((b) => b.classList.contains("live"));
      rows.forEach((b, i) => {
        b.classList.add("inner");
        b.classList.toggle("trail", i < live_at);
        b.classList.toggle("last", i === rows.length - 1);
        list.append(b);
      });
    }
    update_pings();
    $("servers").scrollTop = scroll;
  }

  function update_pings() {
    const list = $("server-list");
    if (!list) return;
    for (const el of list.querySelectorAll("[data-node]")) {
      ping_into(el, pings[el.dataset.node]);
    }
    const groups = new Map(group_servers().map((g) => [g.key, g]));
    for (const el of list.querySelectorAll("[data-group]")) {
      const g = groups.get(el.dataset.group);
      ping_into(el, g ? group_ping(g) : null);
    }
  }

  async function ping_once() {
    if (ping_pending) return;
    ping_pending = true;
    const app = backend();
    if (!app || typeof app.Ping_servers !== "function") { ping_pending = false; return; }
    let answer = null;
    try { answer = await app.Ping_servers(); } catch (e) { answer = null; } finally { ping_pending = false; }
    if (!answer || answer.error) return;
    const now = Date.now();
    for (const p of answer.pings || []) {
      if (typeof p.rtt_ms === "number") pings[p.node_id] = { rtt: p.rtt_ms, at: now };
    }
    save_pings();
    if ($("servers") && !$("servers").hidden) update_pings();
  }

  function start_pings() {
    stop_pings();
    ping_once();
    ping_timer = setInterval(ping_once, 5000);
  }
  function stop_pings() {
    if (ping_timer) clearInterval(ping_timer);
    ping_timer = 0;
  }
  function background_ping(main_active) {
    if (ping_timer || !main_active || document.hidden) return;
    ping_once();
  }

  function place_menu() {
    const pop = $("servers");
    const chip = $("chip");
    if (!pop || !chip) return;
    const r = chip.getBoundingClientRect();
    const below = window.innerHeight - r.bottom - 12;
    const above = r.top - 12;
    const down = below >= 240 || below >= above;
    pop.classList.toggle("up", !down);
    pop.style.maxHeight = Math.max(160, Math.min(420, down ? below : above)) + "px";
  }

  async function load_servers() {
    const app = backend();
    if (!app || typeof app.Servers !== "function") return { error: { code: "not_available" } };
    let answer = null;
    try { answer = await app.Servers(); } catch (e) { answer = null; }
    if (answer && !answer.error) servers = answer.servers || [];
    return answer || { error: { code: "read_failed" } };
  }

  async function open_servers() {
    const serversPop = $("servers");
    if (!serversPop) return;
    if (!serversPop.hidden) { env.close_pops(); return; }
    env.close_pops();
    env.open_pop("servers", "chip");
    place_menu();
    const note = $("servers-note");
    if (servers === null) {
      const list = $("server-list");
      if (list) list.textContent = "";
      if (note) {
        note.hidden = false;
        note.textContent = "Загружаем список…";
      }
    }
    const request = ++list_request;
    const answer = await load_servers();
    if (request !== list_request || serversPop.hidden) return;
    if (answer.error) {
      if (note) {
        note.hidden = false;
        note.textContent = "Список не загрузился: " + (answer.error.message || answer.error.code);
      }
      return;
    }
    if (note) {
      note.hidden = servers.length > 0;
      note.textContent = "Серверов нет.";
    }
    const s = env.get_strategy() || {};
    const picked = server_by_id(s.node_id);
    const here = picked ? picked.country : (s.country || (env.get_status() && env.get_status().country) || "");
    const c = country_of(here);
    if (c.code || c.name) open_groups.add(c.code || c.name);
    const groups = group_servers();
    if (groups.length === 1) open_groups.add(groups[0].key);
    render_server_menu();
    start_pings();
  }

  function init(options) {
    if (options) Object.assign(env, options);
    load_pings();
  }

  const api = {
    init,
    country_of,
    short_name,
    flag_into,
    server_by_id,
    node_label,
    load_servers,
    render_server_menu,
    render_server_policy,
    open_servers,
    place_menu,
    start_pings,
    stop_pings,
    background_ping,
    load_pings,
    live_node,
    reset: () => { servers = null; },
    get_default_pinned: () => default_pinned,
    set_default_pinned: (v) => {
      default_pinned = !!v;
      try { localStorage.setItem("server.default_pinned", default_pinned ? "1" : "0"); } catch (e) {}
    },
    get_servers: () => servers
  };

  root.servers_ui = api;
  if (typeof module !== 'undefined') module.exports = api;
})(typeof window !== 'undefined' ? window : globalThis);
