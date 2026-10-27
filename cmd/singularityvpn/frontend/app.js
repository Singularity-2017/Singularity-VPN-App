// The page renders what the service reports and nothing else. Every number
// shown is a field the service sent; a field that is absent is not drawn,
// never replaced by a zero or a dash.
//
// Each thing on the screen is set in one place, next to itself: automatic
// server choice sits right after the word "Сервер", the country and the
// server are picked from one menu, the anonymity level lives inside its own
// tile, how a goal was applied is shown on that goal's tile, and a hint
// shows on hovering the thing it explains instead of being printed apart
// from it.
(function () {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const backend = () => window.go.main.App;
  const flags = window.flags;

  const screens = ["screen-wait", "screen-service", "screen-untrusted", "screen-signin", "screen-main"];
  // The anonymity levels' pop-up is out of the window with the goal tiles.
  const pops = ["menu", "help", "settings", "servers" /* , "levels" */];
  const pop_owner = { help: "help-button", menu: "menu-button", settings: "settings-button", servers: "chip" /* , levels: "goal-anonymity" */ };
  // What the button says. Connected or not is the ring and this label;
  // nothing else on the screen repeats it.
  const power_text = {
    disconnected: "Подключиться",
    connecting: "Подключение…",
    connected: "Подключено",
    degraded: "Подключено",
    switching: "Переключение…",
    disconnecting: "Отключение…",
    failed: "Подключиться"
  };
  // The line under the button, only where the button alone would mislead.
  const trouble_text = {
    degraded: "Проверяем соединение",
    failed: "Не удалось подключиться"
  };
  const level_text = { "": "Выкл", light: "Лёгкая", standard: "Средняя", high: "Высокая" };
  const reason_text = {
    least_loaded: "выбран наименее загруженный",
    chosen_node: "выбран вами",
    lowest_rtt: "самый быстрый канал"
  };
  const verdict_text = { open: "открыт", no_answer: "не отвечает", no_network: "не отвечает" };
  const method_text = { dns: "блок по DNS", tcp: "блок по IP", tls: "блок по SNI" };
  const why_text = {
    not_supported: "серверы пока этого не дают",
    no_reach_evidence: "о вашей сети пока нет данных",
    chosen_node: "сервер выбран вручную",
    no_client: "ещё нет профиля для замеров",
    not_enough_measurements: "мало замеров, чтобы сравнить"
  };
  // A ping that missed one round keeps its number this long, so a single
  // lost echo does not blank a server for a second; after that it is gone.
  const ping_keep_ms = 3000;
  // The detailed mode is the wide window. The strip on the right resizes the
  // window to one side of this line or the other.
  const wide_query = window.matchMedia("(min-width: 640px)");
  const compact_width = 440, expanded_width = 800;

  let view = null;
  let strategy = null; // as the service last reported it; null until read
  let servers = null;  // the last server list; null until fetched
  let busy = false;
  let loading_strategy = false;
  let strategy_unsupported = false; // a service too old to know the command
  let tunnel = null;    // "vpn" or "antizapret" as the service last reported it; null until read
  let loading_tunnel = false;
  let tunnel_unsupported = false;
  let journal_entries = null; // the service's operations, oldest first; null until read
  let journal_unsupported = false;
  let journal_error = false;
  let compact_journal = false;
  try { compact_journal = localStorage.getItem("compact.panel") === "journal"; } catch (e) {}
  let loading_journal = false;
  let in_match = false; // a game is in a match: moving the tunnel now would end it
  let games = null;     // the game cards as the service last reported them; null until read
  let loading_games = false;
  let games_unsupported = false;
  let games_read_at = 0; // when the game list was last read, for its pings
  let game_region = "";  // the region the selected game is boosted toward; "" for wherever it plays
  // server_reply is a "no" being written about a card's match server: the
  // game, the server it is about and the address typed so far. It
  // outlives a rebuild of the cards.
  let server_reply = null;
  let gamer = null;      // gamer mode: the games are shown; null until known
  let boosting = false;  // a boost is being switched on or off
  let list_request = 0; // the newest list fill; an older answer is dropped
  let analysis = null;  // the last block analysis; null until there is one
  let analysis_request = 0; // the newest analysis call; an older answer is dropped
  let main_active = false;  // the main screen is showing; set on the way in
  let expanded = wide_query.matches;
  let pings = {};       // node_id -> { rtt, at }: answers to the service's echoes
  let ping_timer = 0;
  let ping_pending = false;
  const open_groups = new Set(); // countries unfolded in the server menu
  const region_names = make_region_names("ru");
  const english_names = make_region_names("en");

  function make_region_names(locale) {
    try { return new Intl.DisplayNames([locale], { type: "region" }); } catch (e) { return null; }
  }
  function region_name(names, code) {
    try { return (names && names.of(code)) || ""; } catch (e) { return ""; }
  }

  // country_of is a country field as the menu shows it: an ISO code when
  // one can be made of it, and the Russian name -- or the field as typed
  // when it names no country this page knows.
  function country_of(raw) {
    const code = flags.country_code(raw);
    return { code, name: code ? (region_name(region_names, code) || code) : String(raw || "") };
  }

  // short_name drops the country off the front of a server's name: under
  // Германия, "Germany X" is "X". A name that is nothing but its country
  // falls back to the city, then to itself.
  function short_name(name, raw_country, city) {
    if (!name) return city || "";
    const code = flags.country_code(raw_country);
    const spellings = [raw_country, code, region_name(region_names, code), region_name(english_names, code)]
      .concat(flags.names_for(code))
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

  function server_icon() {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("class", "server-icon");
    svg.setAttribute("viewBox", "0 0 24 24");
    svg.setAttribute("aria-hidden", "true");
    svg.innerHTML = '<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.01M7 16.5h.01"/>';
    return svg;
  }

  function flag_into(el, code) {
    el.textContent = "";
    const svg = flags.flag_svg(code);
    if (svg) { el.innerHTML = svg; return; }
    if (!code) return;
    // No flag drawn for it: the honest two letters instead.
    const box = document.createElement("span");
    box.className = "flag code";
    box.textContent = code;
    el.append(box);
  }

  function load_expanded() {
    try { return localStorage.getItem("expanded") === "1"; } catch (e) { return false; }
  }
  function save_expanded() {
    try { localStorage.setItem("expanded", expanded ? "1" : "0"); } catch (e) { /* the mode is a convenience */ }
  }

  function show_screen(id) {
    if (window.account_ui) window.account_ui.update(id === "screen-main", view && view.status && view.status.username || "");
    for (const s of screens) $(s).hidden = s !== id;
    const main = id === "screen-main";
    if (!main) { main_active = false; if (window.statistics_ui) window.statistics_ui.update(null); }
    $("edge").hidden = !main;
    $("menu-button").hidden = !(main || id === "screen-signin") || !(view && view.status && view.status.signed_in);
    $("settings-button").hidden = !(main || id === "screen-signin");
    $("help-button").hidden = !main;
    if (!main) close_pops();
  }

  function set_text(id, text) {
    const el = $(id);
    el.textContent = text;
    return el;
  }

  // row shows a details row only when it has a value.
  function row(name, value) {
    const present = value !== undefined && value !== null && value !== "";
    $("row-" + name).hidden = !present;
    if (present) set_text("d-" + name, value);
  }

  // --- time ---------------------------------------------------------------
  // A moment is shown as how long ago it was; the full date is on hover.

  function plural(n, one, few, many) {
    const t = n % 10, h = n % 100;
    if (t === 1 && h !== 11) return one;
    if (t >= 2 && t <= 4 && (h < 12 || h > 14)) return few;
    return many;
  }

  function ago(iso) {
    const t = new Date(iso).getTime();
    if (isNaN(t)) return "";
    const s = Math.max(0, Math.round((Date.now() - t) / 1000));
    if (s < 5) return "только что";
    if (s < 60) return s + " с назад";
    const m = Math.floor(s / 60);
    if (m < 60) return m + " мин назад";
    const h = Math.floor(m / 60);
    if (h < 24) return h + " ч назад";
    const d = Math.floor(h / 24);
    return d + " " + plural(d, "день", "дня", "дней") + " назад";
  }

  function full_date(iso) {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return "";
    return d.toLocaleString("ru-RU", { day: "numeric", month: "long", year: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit" });
  }

  // row_time is row for a moment: the value counts up by itself (see
  // tick_times) and carries the full date as its hint.
  function row_time(name, iso) {
    const text = iso ? ago(iso) : "";
    $("row-" + name).hidden = !text;
    const el = $("d-" + name);
    if (!text) { delete el.dataset.time; delete el.dataset.tip; return; }
    el.dataset.time = iso;
    el.dataset.tip = full_date(iso);
    el.textContent = text;
  }

  function tick_times() {
    window.statistics_ui.tick();
    for (const el of document.querySelectorAll("[data-time]")) el.textContent = ago(el.dataset.time);
  }

  const protocol_names = { wireguard: "WireGuard", amneziawg: "AmneziaWG", openvpn: "OpenVPN" };

  function server_by_id(id) {
    return (servers || []).find((s) => s.node_id === id) || null;
  }

  function ms(value) {
    return value === undefined || value === null ? "" : Math.round(value) + " мс";
  }

  // goal_outcome is what the control plane said about one goal for the
  // current connection, or null when there is no connection or it said
  // nothing about that goal.
  function goal_outcome(status, goal) {
    if (!status || !status.session_id || !status.goals) return null;
    return status.goals.find((g) => g.goal === goal) || null;
  }

  function outcome_text(goal, outcome) {
    if (!outcome) return "";
    if (!outcome.applied) return why_text[outcome.why] || outcome.why || "";
    if (goal === "route" && outcome.compared) return "лучший из " + outcome.compared + " замеренных";
    return { bypass: "протокол подобран под вашу сеть", geo: "сервер в этой стране", route: "применено", anonymity: "применено" }[goal];
  }

  function render() {
    apply_theme();
    if (!view) { show_screen("screen-wait"); return; }
    if (view.error && view.error.code === "untrusted_pipe") { show_screen("screen-untrusted"); return; }
    if (!view.service_running) { show_screen("screen-service"); return; }
    const status = view.status;
    if (!status) { show_screen("screen-wait"); return; }
    if (!status.signed_in) { show_screen("screen-signin"); return; }
    show_screen("screen-main");
    if (!main_active) { main_active = true; load_analysis(); background_ping(); }
    if (strategy === null && !strategy_unsupported) load_strategy();
    if (tunnel === null && !tunnel_unsupported) load_tunnel();
    if (games === null && !games_unsupported) load_games();

    const st = status.state;
    const up = st === "connected" || st === "degraded";
    const moving = st === "connecting" || st === "switching" || st === "disconnecting";
    // A game that is on gets its ping measured while connected; read it
    // again every few seconds.
    if (up && games && games.some((g) => g.on) && Date.now() - games_read_at > 5000) load_games();
    const s = strategy || {};
    const auto = !s.node_id;

    // The goal tiles are out of the window until they work.
    // document.querySelector(".goals").hidden = strategy === null;
    render_chip(status, s, auto, up);

    const power = $("power");
    power.className = "power" + (st === "connected" ? " on" : "") + (moving ? " busy" : "") + (st === "degraded" || st === "failed" ? " trouble" : "");
    set_text("power-label", power_text[st] || st);
    const offers_off = up || st === "connecting" || st === "switching";
    power.setAttribute("aria-label", offers_off ? "Отключить VPN" : "Подключить VPN");
    power.disabled = busy || st === "disconnecting";
    const trouble = st === "degraded" ? window.health_text.degraded(status) : trouble_text[st] || "";
    $("trouble").hidden = !trouble;
    set_text("trouble", trouble);

    // The information plate sits under the button, in the detailed mode.
    // Which server and how fast are on the server chip and not repeated here.
    const has_session = !!status.session_id;
    $("details").hidden = !(expanded && has_session);
    window.statistics_ui.update(status);
    row("loss", status.loss_percent === undefined ? "" : Math.round(status.loss_percent) + "%");
    $("row-loss").querySelector(".l").dataset.tip = "Доля неудачных из четырёх TCP-соединений " +
      (status.tunnel === "antizapret" ? "с DNS сервера" : "с внешним адресом") + " через туннель, а не потери пакетов.";
    row("udp", status.udp_loss_percent === undefined ? "" : Math.round(status.udp_loss_percent) + "%" + (status.udp_rtt_ms === undefined ? "" : " · " + Math.round(status.udp_rtt_ms) + " мс"));
    row_time("measured", status.measured_at);

    render_mode(status, s);
    // render_goals(status, s);
    $("gamer").setAttribute("aria-checked", gamer ? "true" : "false");
    render_account(status);
    render_games();
    render_blocks();
    if ((expanded || compact_journal) && !journal_unsupported) load_journal();
    render_journal();

    $("window").classList.toggle("expanded", expanded);
    const edge = $("edge");
    edge.setAttribute("aria-pressed", expanded ? "true" : "false");
    edge.setAttribute("aria-label", expanded ? "Простой режим" : "Подробный режим");
    fit_height_when_layout_changes();
  }

  // The narrow window is as tall as what it shows, top to bottom: fitted
  // once whenever that changes shape -- the first main screen, games
  // arriving, gamer mode on or off -- and never continuously, so nothing
  // the window does by itself fights a person resizing it.
  let fitted_layout = "";
  function fit_height_when_layout_changes() {
    if (expanded) { fitted_layout = ""; return; }
    const layout = [gamer, games ? games.length : -1, compact_journal].join("|");
    if (layout === fitted_layout) return;
    fitted_layout = layout;
    setTimeout(fit_height, 0);
  }
  function fit_height() {
    const app = window.go && window.go.main && window.go.main.App;
    if (!app || typeof app.Set_window_height !== "function") return;
    const box = $("window");
    box.classList.add("measuring");
    const needed = Math.ceil(box.getBoundingClientRect().height);
    box.classList.remove("measuring");
    if (needed > 0) app.Set_window_height(needed);
  }

  // render_chip: what it is connected to; otherwise what was picked;
  // otherwise the country automatic choice is held to, or none. The flag and
  // the country lead, the server's own name follows small, without the
  // country it would repeat.
  function render_chip(status, s, auto, up) {
    let code = "", title = "", sub = [], server = "";
    if (status.session_id) {
      const c = country_of(status.country);
      code = c.code;
      title = c.name || status.node_name || status.node_id;
      server = c.name ? short_name(status.node_name, status.country, status.city) : "";
      if (server) sub.push(server);
      const protocol = protocol_names[status.protocol] || status.protocol || "";
      if (protocol) sub.push(protocol);
    } else if (!auto) {
      const picked = server_by_id(s.node_id);
      if (picked) {
        const c = country_of(picked.country);
        code = c.code;
        title = c.name || picked.name || picked.node_id;
        server = c.name ? short_name(picked.name, picked.country, picked.city) : "";
        if (server) sub.push(server);
      } else {
        title = "Выбран вручную";
      }
    } else if (strategy !== null) {
      if (s.country) {
        const c = country_of(s.country);
        code = c.code;
        title = c.name;
      } else {
        title = "Все страны";
      }
    }
    $("chip").hidden = !title;
    // Who chooses the server is said on the server itself. Until the
    // strategy is known nothing claims to be automatic or not.
    const badge = $("chip-badge");
    badge.hidden = strategy === null;
    badge.className = "chip-badge" + (auto ? " auto" : "");
    badge.textContent = !auto ? "Закреплён" : s.country ? "Авто" : "Авто · страна";
    badge.dataset.tip = !auto
      ? "Сервер выбран вами и не меняется сам. Чтобы снова выбирать автоматически, выберите в списке «Авто»."
      : s.country
        ? "Сервер в этой стране выбирается автоматически и меняется, если перестал работать. Чтобы закрепить, выберите сервер в списке."
        : "Страна и сервер выбираются автоматически и меняются, если сервер перестал работать. Чтобы закрепить, выберите страну или сервер в списке.";
    $("chip").classList.toggle("auto", auto && strategy !== null);
    flag_into($("chip-flag"), code);
    set_text("place", title);
    const chip_sub = $("chip-sub");
    chip_sub.textContent = sub.join(" · ");
    // A server's name is a letter or two, which says nothing by itself; the
    // icon in front of it says what it names.
    if (server) chip_sub.prepend(server_icon());
    const has_rtt = up && status.rtt_ms !== undefined && status.rtt_ms !== null;
    if (has_rtt) {
      if (sub.length) chip_sub.append(" · ");
      const rtt = document.createElement("span");
      rtt.className = "num accent";
      rtt.textContent = ms(status.rtt_ms);
      rtt.dataset.tip = status.tunnel === "antizapret"
        ? "Задержка через туннель: время TCP-соединения с DNS сервера."
        : "Задержка через туннель: время TCP-соединения с внешним адресом, а не ping до сервера.";
      chip_sub.append(rtt);
    }
    chip_sub.hidden = !(sub.length || has_rtt);
    const reason = status.session_id && status.reason ? reason_text[status.reason] || "" : "";
    $("reason").hidden = !(expanded && reason);
    set_text("reason", reason);
  }

  function render_goals(status, s) {
    const known = strategy !== null;
    for (const id of ["goal-bypass", "goal-route", "goal-anonymity"]) $(id).disabled = !known || busy;
    $("goal-bypass").setAttribute("aria-pressed", s.bypass ? "true" : "false");
    $("goal-route").setAttribute("aria-pressed", s.route ? "true" : "false");
    $("goal-anonymity").classList.toggle("on", !!s.anonymity);
    set_text("anon-value", level_text[s.anonymity || ""]);

    // How each goal fared is shown on the goal itself, in the detailed
    // mode -- except that an anonymity level which does nothing yet says so
    // in both modes: a level that only looks like protection must not be
    // mistaken for it.
    const subs = { bypass: s.bypass, route: s.route, anonymity: !!s.anonymity };
    for (const goal of Object.keys(subs)) {
      let text = subs[goal] && expanded ? outcome_text(goal, goal_outcome(status, goal)) : "";
      const applied = goal_outcome(status, goal);
      const idle_level = goal === "anonymity" && s.anonymity && !(applied && applied.applied);
      if (idle_level) text = "пока не действует";
      const el = $("sub-" + goal);
      el.hidden = !text;
      el.textContent = text;
      el.classList.toggle("ok", !!(applied && applied.applied));
      if (idle_level) el.dataset.tip = "Пока ни один сервер не даёт дополнительной анонимности. Уровень запомнится и начнёт действовать, когда это появится.";
      else delete el.dataset.tip;
    }
  }

  // Preserve historical direct checks in both layouts, including unknown states.
  let services_editing = false, services_list = null, services_busy = false, services_message = "";
  const services_errors = { invalid_host: "Это не похоже на адрес сайта.", too_many: "Своих сервисов уже слишком много.", duplicate: "Такой сервис уже в списке.", not_removable: "Этот адрес нужен для самой проверки, его не убрать.", unknown_service: "Такого сервиса в списке уже нет." };
  let services_open = false, analysis_loading = false, analysis_error = false, analysis_stale = false;
  try { services_open = localStorage.getItem("services.open") === "1"; } catch (e) {}
  function render_blocks() {
    const el = $("blocks");
    el.hidden = false;
    const checks = analysis && analysis.checks ? analysis.checks : [];
    const unavailable = checks.filter(c => ["blocked", "no_answer", "no_network"].includes(c.verdict));
    const unknown = checks.some(c => !["open", "blocked", "no_answer", "no_network"].includes(c.verdict));
    const text = analysis_loading ? "Сервисы · проверяем…" : !checks.length ? "Сервисы · нет проверки" : unavailable.length ? "Сервисы · недоступны: " + unavailable.length : unknown ? "Сервисы · часть не проверена" : "Сервисы · проверенные доступны";
    const line = set_text("blocks-line", text + (analysis_stale && checks.length ? " · прошлый замер" : "") + (services_open ? " ‹" : " ›"));
    line.setAttribute("aria-expanded", String(services_open));
    line.classList.toggle("clear", unavailable.length === 0);
    line.dataset.tip = "Последняя проверка напрямую, без VPN. Результат не описывает доступность через подключённый сервер." + (analysis_stale ? " После проверки сеть могла измениться." : "");
    $("blocks-details").hidden = !services_open;
    const refresh = $("blocks-refresh");
    const down = view && view.status && ["disconnected", "failed"].includes(view.status.state);
    refresh.disabled = analysis_loading || !down;
    refresh.dataset.tip = down ? "Проверить DNS, TCP и TLS напрямую" : "Прямая проверка доступна после отключения VPN";
    if (!services_open) return;
    const rows = $("blocks-rows");
    rows.replaceChildren();
    if (!checks.length || analysis_error || analysis_stale) {
      const note = document.createElement("div"); note.className = "pop-note";
      note.textContent = analysis_error ? "Не удалось обновить проверку." : !checks.length ? "Сервисы ещё не проверены." : "Показан предыдущий замер; сеть могла измениться.";
      rows.append(note);
    }
    // With the person's list at hand, it decides what is shown: a service
    // taken out disappears at once, one just added waits for a measurement.
    const listed = services_list && services_list.targets ? services_list.targets.filter(t => !t.control) : null;
    const shown = listed ? listed.map(t => checks.find(c => c.key === t.key) || { key: t.key, label: t.label || t.host }) : checks;
    for (const c of [...shown].sort((a,b) => Number(a.verdict === "open") - Number(b.verdict === "open"))) {
      const r = document.createElement("div"); r.className = "row";
      const label = document.createElement("span"); label.className = "l"; label.textContent = c.label || c.host;
      const value = document.createElement("span");
      value.textContent = c.verdict === "blocked" ? (method_text[c.method] || "заблокирован") : (verdict_text[c.verdict] || "не проверено");
      if (c.verdict !== "open") value.className = "bad";
      r.append(label, value);
      if (services_editing && listed) {
        const remove = document.createElement("button");
        remove.type = "button"; remove.className = "service-remove"; remove.textContent = "×";
        remove.disabled = services_busy;
        remove.setAttribute("aria-label", "Убрать " + (c.label || c.host));
        remove.addEventListener("click", () => services_call(() => backend().Remove_service(c.key)));
        r.append(remove);
      }
      rows.append(r);
    }
    const edit = $("services-edit");
    edit.textContent = services_editing ? "Готово" : "Изменить список";
    edit.setAttribute("aria-expanded", String(services_editing));
    $("services-editor").hidden = !services_editing;
    const removed = services_list && services_list.removed_popular > 0 ? services_list.removed_popular : 0;
    const restore = $("services-restore");
    restore.hidden = !removed;
    restore.disabled = services_busy;
    restore.textContent = "Вернуть популярные · " + removed;
    $("services-status").hidden = !services_message;
    $("services-status").textContent = services_message;
    row_time("blocks-measured", analysis && analysis.measured_at);
  }

  // The list of services is the person's: the popular ones minus those taken
  // out, plus their own. It is changed in place, in the open services list.

  async function services_call(action) {
    if (services_busy) return false;
    services_busy = true; render_blocks();
    let answer;
    try { answer = await action(); } catch (e) { answer = null; }
    services_busy = false;
    const ok = !!answer && !answer.error;
    if (ok) { services_list = answer.services || answer; services_message = ""; }
    else services_message = answer && answer.error && services_errors[answer.error.code] || "Не удалось получить или изменить список.";
    render_blocks();
    return ok;
  }

  // load_services reads the list quietly: without it the measured checks
  // are shown as they are.
  async function load_services() {
    await services_call(() => backend().Services());
    if (!services_editing) services_message = "";
    render_blocks();
  }

  async function toggle_services_edit() {
    services_editing = !services_editing;
    services_message = "";
    if (services_editing) { await services_call(() => backend().Services()); if (services_editing) $("services-host").focus(); }
    else render_blocks();
  }

  async function refresh_analysis() {
    if (analysis_loading || !view || !view.status || !["disconnected", "failed"].includes(view.status.state)) return;
    // Anything else bending the path would be measured instead of the provider.
    const go = await ask("Проверка идёт напрямую. Перед ней выключите сторонние VPN, Антизапрет и обход DPI (zapret, GoodbyeDPI и подобные) — иначе замер покажет их, а не вашего провайдера.", "Проверить");
    if (!go || analysis_loading || !view || !view.status || !["disconnected", "failed"].includes(view.status.state)) return;
    analysis_loading = true; analysis_error = false; render_blocks();
    const request = ++analysis_request;
    let answer;
    try { answer = await backend().Analyze(); } catch (e) { answer = null; }
    analysis_loading = false;
    if (request !== analysis_request) return;
    analysis_error = !answer || !!answer.error || !answer.result;
    if (!analysis_error && answer.result) { analysis = answer.result; analysis_stale = false; }
    render_blocks();
  }

  // load_analysis runs when the main screen comes up: it reads what the
  // service holds. The service measures on its own, once a day per network
  // while no connection is up; the window never starts one unasked. Any
  // error leaves the previous result visible. A newer call makes an older
  // answer irrelevant.
  async function load_analysis() {
    const request = ++analysis_request;
    let answer;
    try { answer = await backend().Analysis(); } catch (e) { answer = { error: { code: "failed" } }; }
    if (request !== analysis_request) return;
    analysis_error = !!(answer && answer.error);
    if (!analysis_error) { analysis = answer && answer.result || null; analysis_stale = !!(answer && answer.stale); }
    render();
  }

  // reread_analysis picks up a measurement the service took on its own, after
  // a network change. It only reads -- never Analyze -- and draws only when
  // the answer is newer than what is shown. A load_analysis that started
  // meanwhile owns the line, so this answer is dropped under the same guard.
  async function reread_analysis() {
    if (!main_active) return;
    const request = analysis_request;
    let answer;
    try { answer = await backend().Analysis(); } catch (e) { return; }
    if (request !== analysis_request || !main_active) return;
    if (!answer || answer.error || !answer.result) return;
    const incoming = new Date(answer.result.measured_at).getTime();
    if (isNaN(incoming)) return;
    const shown = analysis ? new Date(analysis.measured_at).getTime() : NaN;
    if (!isNaN(shown) && (incoming < shown || (incoming === shown && analysis_stale === !!answer.stale))) return;
    analysis_stale = !!answer.stale;
    analysis = answer.result;
    render();
  }

  async function refresh() {
    try {
      view = await backend().Status();
    } catch (e) {
      view = { service_running: false };
    }
    render();
  }

  async function load_strategy() {
    if (loading_strategy) return;
    loading_strategy = true;
    let answer = null;
    try { answer = await backend().Strategy(); } catch (e) { answer = null; } finally { loading_strategy = false; }
    if (answer && answer.error && answer.error.code === "unknown_command") strategy_unsupported = true;
    if (answer && answer.strategy) {
      strategy = answer.strategy;
      if (strategy.node_id && servers === null) load_servers().then(render);
      render();
    }
  }

  async function load_servers() {
    let answer;
    try { answer = await backend().Servers(); } catch (e) { answer = { error: { code: "failed", message: String(e) } }; }
    if (answer && !answer.error) servers = answer.servers || [];
    return answer || { error: { code: "failed" } };
  }

  function show_error(id, outcome) {
    const el = $(id);
    if (!outcome || !outcome.code) { el.hidden = true; return; }
    const known = {
      unauthorized: "Неверное имя пользователя или пароль.",
      busy: "Служба занята предыдущей командой. Попробуйте через несколько секунд.",
      not_signed_in: "Нужно войти заново.",
      service_not_running: "Служба не запущена.",
      untrusted_pipe: "Канал к службе не прошёл проверку.",
      server_unavailable: "Этот сервер сейчас не принимает подключения. Выберите другой или включите «Авто».",
      launch_failed: "Steam не запустил игру. Буст включён: запустите её сами.",
      no_server_in_country: "В этой стране сейчас нет доступного сервера."
    };
    el.textContent = known[outcome.code] || ("Не получилось: " + (outcome.message || outcome.code));
    el.hidden = false;
  }

  // ask shows a short question over the window and resolves to the answer.
  function ask(text, yes) {
    return new Promise((resolve) => {
      set_text("confirm-text", text);
      set_text("confirm-yes", yes);
      const previous_focus = document.activeElement;
      $("confirm").hidden = false;
      const keydown = (event) => { if (event.key === "Escape") { event.stopImmediatePropagation(); done(false); } };
      document.addEventListener("keydown", keydown, true);
      $("confirm-no").focus();
      const done = (answer) => {
        $("confirm").hidden = true;
        document.removeEventListener("keydown", keydown, true);
        if (previous_focus && previous_focus.getClientRects().length) previous_focus.focus();
        $("confirm-yes").onclick = null;
        $("confirm-no").onclick = null;
        resolve(answer);
      };
      $("confirm-yes").onclick = () => done(true);
      $("confirm-no").onclick = () => done(false);
    });
  }

  // change records a new strategy. Giving up high anonymity is the one
  // change worth a question; everything else just happens.
  async function change(edit) {
    if (strategy === null || busy) return;
    const next = Object.assign({}, strategy, edit);
    if (strategy.anonymity === "high" && next.anonymity !== "high") {
      if (!(await ask("Снять высокий уровень анонимности?", "Снять"))) return;
    }
    // A match ends when the tunnel moves to another server. Pinning the
    // server already connected to moves nothing.
    const moves = ("node_id" in edit && edit.node_id !== live_node()) || ("country" in edit && edit.country !== strategy.country);
    if (in_match && moves) {
      if (!(await ask("Идёт матч. Сменить сервер? Матч прервётся.", "Сменить"))) return;
    }
    close_pops();
    busy = true;
    show_error("action-error", null);
    render();
    const outcome = await backend().Set_strategy(next);
    busy = false;
    // The record stands even when moving the connection failed; read back
    // what the service now holds rather than assuming.
    await load_strategy();
    show_error("action-error", outcome);
    await refresh();
  }

  // --- the tunnel mode ---------------------------------------------------
  // Full VPN or AntiZapret: which of the node's two profiles the tunnel is
  // built from. The control shows the wish; the note under it says when the
  // live tunnel carries something else, and why.

  async function load_tunnel() {
    if (loading_tunnel || tunnel_unsupported) return;
    const app = backend();
    if (!app || typeof app.Tunnel_mode !== "function") { tunnel_unsupported = true; return; }
    loading_tunnel = true;
    let answer = null;
    try { answer = await app.Tunnel_mode(); } catch (e) { answer = null; } finally { loading_tunnel = false; }
    if (answer && answer.error && answer.error.code === "unknown_command") tunnel_unsupported = true;
    if (answer && answer.mode) { tunnel = answer.mode; render(); }
  }

  function render_mode(status, s) {
    $("mode").hidden = tunnel === null;
    if (tunnel === null) return;
    for (const b of $("mode").querySelectorAll("[data-mode]")) {
      b.setAttribute("aria-checked", b.dataset.mode === tunnel ? "true" : "false");
      b.disabled = busy;
    }
    // What the live tunnel carries, when it is not what was chosen.
    let note = "", tip = "";
    const live = status.session_id ? status.tunnel : "";
    // A boosted game is carried beside the chosen mode, not instead of it.
    if (live && live !== "games" && live !== tunnel) {
      if (live === "vpn" && s.anonymity) {
        note = "Сейчас весь трафик: включена анонимность";
        tip = "Антизапрет оставляет большую часть трафика без VPN, а это противоречит анонимности. Пока она включена, через сервер идёт всё.";
      } else {
        note = live === "vpn" ? "Сейчас: полный VPN" : "Сейчас: антизапрет";
      }
    }
    const el = $("mode-note");
    el.hidden = !note;
    el.textContent = note;
    if (tip) el.dataset.tip = tip; else delete el.dataset.tip;
  }

  async function change_tunnel(mode) {
    if (tunnel === null || busy || mode === tunnel) return;
    close_pops();
    busy = true;
    show_error("action-error", null);
    render();
    const outcome = await backend().Set_tunnel_mode(mode);
    busy = false;
    tunnel = null;
    await load_tunnel();
    show_error("action-error", outcome);
    await refresh();
  }

  // --- the journal -------------------------------------------------------
  // The service's operations in words: what was asked, which server and
  // protocol were tried, what failed and why. The service keeps them in
  // its own terms; the sentences are made here.

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
      if (servers === null && journal_entries.some((e) => e.attrs && e.attrs.node_id)) load_servers().then(render_journal);
    }
    render_journal();
  }

  function node_label(id) {
    const s = server_by_id(id);
    if (!s) return id ? "сервер " + id.slice(0, 8) : "сервер";
    const c = country_of(s.country);
    return c.name ? c.name + " · " + label_of(s) : label_of(s);
  }

  function protocol_label(p) {
    return protocol_names[p] || p || "";
  }

  function bytes_text(n) {
    n = Number(n);
    if (!isFinite(n)) return "";
    if (n < 1024) return n + " Б";
    return (n / 1024).toFixed(n < 10240 ? 1 : 0).replace(".", ",") + " КБ";
  }

  // transport_text renders the adapter's transport report: whether the
  // node answered the handshake, and how much went each way.
  function transport_text(report) {
    if (!report) return "";
    const sent = /sent (\d+) bytes/.exec(report), received = /received (\d+)/.exec(report);
    const amounts = sent && received ? "отправлено " + bytes_text(sent[1]) + ", получено " + bytes_text(received[1]) : "";
    if (/no handshake/.test(report)) return "сервер не ответил на рукопожатие" + (amounts ? "; " + amounts : "");
    if (/handshake/.test(report)) return "рукопожатие прошло, но данные назад почти не идут" + (amounts ? ": " + amounts : "");
    return report;
  }

  function list_of(value) {
    return (value || "").replace(/^\[|\]$/g, "").split(/[\s,]+/).filter(Boolean);
  }

  const tunnel_text = { vpn: "весь трафик", antizapret: "Антизапрет", games: "игры и Антизапрет" };

  const event_text = {
    profile_changed: ["Профиль изменён", "ok"],
    network_changed: ["Сеть изменилась", "net"],
    connection_established: ["Подключено", "ok"],
    connection_lost: ["Связь с сервером потеряна", "warn"],
    connection_degraded: ["Проверяем соединение", "warn"],
    switch_started: ["Переключение на другой сервер", ""],
    switch_completed: ["Переключено", "ok"],
    switch_failed: ["Переключиться не удалось", "warn"]
  };

  // journal_line turns one entry into [text, tone, tip]. An entry the
  // window has no words for -- a warning added to the service later -- is
  // shown as the service wrote it rather than hidden.
  function journal_line(e) {
    const a = e.attrs || {};
    const p = protocol_label(a.protocol);
    switch (e.message) {
      case "service starting": return ["Служба запущена", ""];
      case "service stopped": return ["Служба остановлена", ""];
      case "service stopped with an error": return ["Служба остановлена с ошибкой", "warn", a.error];
      case "service ended without stopping": return ["Служба завершилась, не остановившись: сбой или выключение компьютера", "warn"];
      case "connect requested": return [a.node_id ? "Подключение к " + node_label(a.node_id) : "Подключение, сервер выбирается автоматически", ""];
      case "disconnect requested": return ["Отключение по вашей команде", ""];
      case "control plane recommendation": {
        const why = reason_text[a.reason] ? ", " + reason_text[a.reason] : "";
        return ["Сервер: " + node_label(a.node_id) + why + "; рекомендован " + p, ""];
      }
      case "using an offered alternative this build speaks":
        return ["Рекомендованного протокола в приложении пока нет — пробуем " + p, ""];
      case "using the offered protocol that worked last on this node":
        return ["Пробуем " + p + ": в прошлый раз на этом сервере сработал он", ""];
      case "verification failed":
        return [p + " поднят, но трафик не идёт: " + transport_text(a.transport) + ". Пробуем дальше", "warn", a.transport];
      case "reconnected at start: the machine was left connected": return ["Подключение восстановлено после запуска", "ok"];
      case "retry on the same node failed to connect": return ["Переподключиться к тому же серверу не удалось", "warn", a.err];
      case "retry on the same node failed verification": return ["Переподключились к тому же серверу, но трафик не идёт", "warn", a.err];
      case "network changed; moving the endpoint route failed": return ["Сеть изменилась, а маршрут к серверу перенести не удалось", "warn", a.error];
      case "strategy set": {
        const parts = [a.node_id ? "сервер закреплён: " + node_label(a.node_id) : "сервер — автоматически"];
        if (a.route === "true") parts.push("самый быстрый маршрут");
        if (a.bypass === "true") parts.push("обход блокировок");
        if (a.anonymity) parts.push("анонимность: " + (level_text[a.anonymity] || a.anonymity).toLowerCase());
        if (a.country) parts.push("страна: " + (country_of(a.country).name || a.country));
        return ["Настройки: " + parts.join(", "), ""];
      }
      case "games set": {
        const ids = list_of(a.games);
        const names = ids.map((id) => ((games || []).find((g) => g.id === id) || {}).name || id);
        return [names.length ? "Через сервер идут игры: " + names.join(", ") : "Игры выключены", ""];
      }
      case "tunnel mode set": return ["Режим: " + (a.mode === "antizapret" ? "Антизапрет" : "VPN"), ""];
      case "game router started": return ["Игры отделены от остального трафика", "ok"];
      case "game router stopped": return ["Разделение игр остановлено", ""];
      case "game router: capture stopped; games go direct until the next connect":
        return ["Перехват игр остановился — до следующего подключения игры идут напрямую", "warn", a.error];
      case "event": {
        if (a.kind === "connection_degraded") return [window.health_text.degraded(a), "warn"];
        // One bad sample the next check did not confirm: still a failure.
        if (a.kind === "connection_blip") return ["Короткий провал · " + window.health_text.degraded(a), "warn"];
        const t = event_text[a.kind];
        if (!t) return [a.kind, ""];
        // A tunnel that came up says where and how.
        if (a.node_id && (a.kind === "connection_established" || a.kind === "switch_completed")) {
          const how = [node_label(a.node_id), p, tunnel_text[a.tunnel]].filter(Boolean).join(", ");
          return [t[0] + ": " + how, t[1]];
        }
        return [t[0], t[1]];
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

  function render_journal() {
    const box = $("journal");
    const show = expanded ? !!(journal_entries && journal_entries.length) : compact_journal;
    const parent = expanded ? document.querySelector(".side") : $("games");
    if (box.parentElement !== parent) parent.append(box);
    $("compact-journal").hidden = expanded;
    $("compact-journal").setAttribute("aria-expanded", String(!expanded && compact_journal));
    $("journal-title").disabled = expanded;
    $("journal-note").textContent = journal_unsupported || journal_error ? "Журнал недоступен" : journal_entries === null && loading_journal ? "Загрузка…" : !journal_entries || !journal_entries.length ? "Записей пока нет" : "";
    $("journal-note").hidden = !$("journal-note").textContent;
    $("journal-retry").hidden = !journal_error || journal_unsupported;
    box.hidden = !show;
    if (!show) return;
    const list = $("journal-list");
    const entries = journal_entries || [];
    const key = entries.length + ":" + (entries[entries.length - 1] || {}).at + ":" + (servers ? servers.length : -1);
    if (list.dataset.key === key) return;
    list.dataset.key = key;
    list.textContent = "";
    for (let i = entries.length - 1; i >= 0; i--) {
      const e = entries[i];
      let [text, tone, tip] = journal_line(e);
      // Short blips with nothing between them are one line. Each is still
      // counted in the statistics, and each is listed on hover.
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

  // --- games ---------------------------------------------------------------
  // Shown in gamer mode only. A card per game the catalog offers: pressing
  // the cover boosts the game -- it alone goes through the server, the rest
  // of the internet as without the VPN -- and starts it. The regions under
  // the cover boost toward that region's servers without starting anything;
  // pressing the chosen one again boosts toward wherever the game plays.

  async function load_games() {
    if (loading_games || games_unsupported) return;
    const app = backend();
    if (!app || typeof app.Games !== "function") { games_unsupported = true; return; }
    loading_games = true;
    let answer = null;
    try { answer = await app.Games(); } catch (e) { answer = null; } finally { loading_games = false; }
    if (answer && answer.error && answer.error.code === "unknown_command") games_unsupported = true;
    if (answer && !answer.error) {
      games = answer.games || [];
      game_region = answer.region || "";
      in_match = !!answer.in_match;
      games_read_at = Date.now();
      render();
    }
  }

  // Gamer mode is the person's choice, kept between runs. Until they make
  // it, it is what the installation says: on when the setup came from the
  // gamers' page.
  async function load_gamer() {
    let saved = null;
    try { saved = localStorage.getItem("gamer"); } catch (e) { saved = null; }
    if (saved === "1" || saved === "0") { gamer = saved === "1"; return; }
    const app = backend();
    try { gamer = !!(app && typeof app.Gamer_default === "function" && await app.Gamer_default()); } catch (e) { gamer = false; }
  }

  // A window made so short that the journal has under two lines left folds
  // it back to the games. Only shrinking does: a journal just opened, before
  // the window has grown for it, is not folded.
  const journal_min_px = 48;
  let last_window_height = 0;
  new ResizeObserver(() => {
    const height = $("window").clientHeight;
    const shrank = height < last_window_height;
    last_window_height = height;
    if (!shrank || expanded || !compact_journal || $("journal").hidden || $("window").classList.contains("measuring")) return;
    if ($("journal-list").getBoundingClientRect().height < journal_min_px) toggle_journal();
  }).observe($("window"));

  function toggle_journal() {
    const old = compact_journal ? $("journal") : $("game-cards");
    const parent = $("games");
    const rect = old.getBoundingClientRect(), base = parent.getBoundingClientRect();
    const snapshot = old.cloneNode(true);
    snapshot.removeAttribute("id");
    snapshot.querySelectorAll("[id]").forEach(el => el.removeAttribute("id"));
    snapshot.setAttribute("aria-hidden", "true");
    snapshot.inert = true;
    snapshot.style.cssText = `position:absolute;left:${rect.left-base.left}px;top:${rect.top-base.top}px;width:${rect.width}px;height:${rect.height}px;pointer-events:none;z-index:1;`;
    compact_journal = !compact_journal;
    try { localStorage.setItem("compact.panel", compact_journal ? "journal" : "games"); } catch (e) {}
    render();
    const next = compact_journal ? $("journal") : $("game-cards");
    if (!window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      parent.append(snapshot);
      const duration = 100;
      next.animate([{ opacity: 0 }, { opacity: 1 }], { duration, easing: "ease-out" });
      snapshot.animate([{ opacity: 1 }, { opacity: 0 }], { duration, easing: "ease-out" }).finished.then(() => snapshot.remove(), () => snapshot.remove());
    }
    if (!compact_journal) $("compact-journal").focus();
  }

  function toggle_gamer() {
    if (!expanded && compact_journal) { toggle_journal(); return; }
    gamer = !gamer;
    try { localStorage.setItem("gamer", gamer ? "1" : "0"); } catch (e) { /* kept for this run only */ }
    if (!$("settings").hidden) render_themes();
    render();
  }

  // --- themes ----------------------------------------------------------------
  // A theme is chosen for each mode on its own: the ordinary window keeps
  // its own, gamer mode its own. Sharp themes have no rounding anywhere.
  // The calm themes belong to the normal mode, the sharp ones to Gamer
  // mode; each mode offers only its own.
  const themes = [
    { id: "singularity", name: "Сингулярность", note: "бирюза", swatch: "#4be3df" },
    { id: "graphite", name: "Графит", note: "белое на сером", swatch: "#eef1f3" },
    { id: "amber", name: "Янтарь", note: "тёплый", swatch: "#ffb547" },
    { id: "berserk", name: "Берсерк", note: "красный", swatch: "#ff3b5c", sharp: true },
    { id: "toxin", name: "Токсин", note: "кислотный", swatch: "#a6ff2e", sharp: true },
    { id: "synthwave", name: "Синтвейв", note: "неон на фиолетовом", swatch: "#ff4fd8", sharp: true },
  ];
  const default_theme = { normal: "singularity", gamer: "berserk" };

  function theme_mode() { return gamer ? "gamer" : "normal"; }

  function themes_of(mode) { return [...themes.filter((t) => !!t.sharp === (mode === "gamer")), ...theme_editor.store.custom_themes(mode)]; }

  function theme_of(mode) {
    const id = theme_editor.store.selected(mode);
    const own = themes_of(mode);
    return own.find((t) => t.id === id) || own.find((t) => t.id === default_theme[mode]);
  }

  function apply_theme() {
    const t = theme_of(theme_mode());
    document.documentElement.dataset.theme = t.id;
    document.documentElement.classList.toggle("sharp", !!t.sharp);
    theme_editor.apply(theme_mode(), t.id);
  }

  function pick_theme(id) {
    theme_editor.store.select(theme_mode(), id);
    apply_theme();
    render_themes();
  }

  // A pop-up shows one level at a time: its own rows, or the list one of
  // them opens, with the way back on top.
  function show_level(pop, level) {
    for (const el of $(pop).querySelectorAll(".pop-level")) el.hidden = el.id !== level;
  }

  function render_themes() {
    set_text("theme-title", gamer ? "Тема · Gamer mode" : "Тема · обычный режим");
    set_text("theme-row-name", gamer ? "Тема · Gamer mode" : "Тема");
    set_text("theme-value", theme_of(theme_mode()).name);
    const current = theme_of(theme_mode()).id;
    theme_editor.render(theme_mode(), current);
    const list = $("theme-list");
    list.textContent = "";
    for (const t of themes_of(theme_mode())) {
      const b = option(t.name, t.id === current, () => pick_theme(t.id));
      const swatch = document.createElement("span");
      swatch.className = "swatch" + (t.sharp ? " sharp" : "");
      swatch.style.background = theme_editor.settings(theme_mode(), t.id).accent;
      b.prepend(swatch);
      const name = b.querySelector(".opt-name");
      name.classList.add("two");
      const note = document.createElement("span");
      note.className = "opt-sub";
      note.textContent = t.note;
      name.append(note);
      list.append(b);
    }
  }

  function toggle_settings() {
    const open = $("settings").hidden;
    close_pops();
    if (!open) return;
    render_themes();
    show_level("settings", "settings-main");
    open_pop("settings", "settings-button");
    load_version();
  }

  // --- profiles ---------------------------------------------------------------
  // Which of the account's profiles this computer connects with. One
  // profile on two devices at once moves the older one off, so taking one
  // that is in use elsewhere is asked first.

  async function load_profiles() {
    const app = backend();
    if (!app || typeof app.Profiles !== "function") return;
    const answer = await app.Profiles();
    const list = $("profile-list");
    const note = $("profile-note");
    list.textContent = "";
    if (!answer || answer.error) {
      note.hidden = false;
      note.textContent = "Список профилей сейчас не получить.";
      return;
    }
    const profiles = answer.profiles || [];
    const current = profiles.find((p) => p.current);
    set_text("profile-value", current ? current.label || "Без имени" : "");
    note.hidden = profiles.length > 0;
    note.textContent = profiles.length ? "" : "Профиль появится при первом подключении.";
    for (const p of profiles) {
      const b = option(p.label || "Без имени", !!p.current, () => choose_profile(p));
      b.disabled = busy;
      if (p.online === true && !p.current) {
        const tag = document.createElement("span");
        tag.className = "opt-note";
        tag.textContent = "на другом устройстве";
        b.append(tag);
      }
      list.append(b);
    }
  }

  async function choose_profile(p) {
    if (busy) return;
    if (p.current) { close_pops(); return; }
    if (p.online === true && !(await ask("Профиль «" + (p.label || "Без имени") + "» сейчас подключён на другом устройстве. Если взять его здесь, то устройство отключится.", "Взять профиль"))) return;
    busy = true;
    render();
    const outcome = await backend().Set_profile(p.id);
    busy = false;
    show_error("action-error", outcome);
    if (!outcome || (!outcome.code && !outcome.error)) close_pops();
    await load_profiles();
    await refresh();
  }

  // render_account fills the account circle and its menu: who is signed in
  // and as which device. A name the service does not know is not made up.
  function render_account(status) {
    window.account_ui.render_identity(status);
  }

  // game_line is what a card says under the name: what its boost is doing.
  function game_line(g, st) {
    if (boosting) return { text: "Включаем буст…" };
    if (!g.on) return { text: "" };
    const direct = (g.regions || []).find((r) => r.id === game_region && r.direct);
    if (direct) return { text: "Напрямую · " + direct.name, on: true };
    if (st === "connecting" || st === "switching") return { text: "Буст включается…" };
    if (st !== "connected" && st !== "degraded") return { text: "Буст выключен: VPN не подключён" };
    const region = (g.regions || []).find((r) => r.id === game_region);
    return { text: "Буст · " + (region ? region.name : "любой регион"), on: true };
  }

  // The games section stands whenever the catalog offers a game: its
  // heading is the gamer mode switch, and the cards show in gamer mode.
  function render_games() {
    $("games").hidden = !(gamer !== null && games && games.length) && !(!expanded && compact_journal);
    $("gamer").hidden = !(games && games.length);
    $("game-cards").hidden = !gamer || (!expanded && compact_journal);
    if ($("games").hidden || !gamer) return;
    const st = view && view.status ? view.status.state : "";
    const box = $("game-cards");
    // The cards are rebuilt only when what they show changes, so one under
    // the pointer or the keyboard keeps its hover and its focus.
    const key = games.map((g) => g.id + (g.on ? "+" : "-") + (g.ping_at || "") +
      (g.server_offer ? "@" + g.server_offer.server : "") + (g.detection_confirmed ? "#" + g.detection_right + "/" + g.detection_confirmed : "")).join(",") +
      "|" + game_region + "|" + st + (busy ? "!" : "") + (expanded ? "w" : "") + (server_reply ? "?" + server_reply.game + server_reply.server : "");
    if (box.dataset.key === key) return;
    box.dataset.key = key;
    box.textContent = "";
    for (const g of games) {
      const line = game_line(g, st);
      const card = document.createElement("div");
      card.className = "game-card" + (line.on ? " on" : "");
      const content = document.createElement("div");
      content.className = "game-card-content";
      content.tabIndex = 0;
      content.setAttribute("role", "group");
      content.setAttribute("aria-label", g.name);
      card.append(content);

      const cover = document.createElement("button");
      cover.className = "game-cover";
      cover.style.backgroundImage = "url(covers/" + encodeURIComponent(g.id) + ".svg)";
      card.style.setProperty("--game-art", cover.style.backgroundImage);
      cover.disabled = busy;
      cover.setAttribute("aria-label", g.name + ": " + line.text);
      const name = document.createElement("span");
      name.className = "game-name";
      name.textContent = g.name;
      const sub = document.createElement("span");
      sub.className = "game-line" + (line.cta ? " cta" : "");
      sub.textContent = line.text;
      sub.hidden = !line.text;
      if (line.on && g.ping_ms != null && g.ping_at) {
        // The game's own ping: to the server it was playing on, through
        // the VPN server, measured while a match was on.
        const value = document.createElement("span");
        value.className = "num";
        value.textContent = Math.round(g.ping_ms) + " мс";
        value.dataset.tip = g.ping_first_mile_ms != null && g.ping_node_ms != null
          ? "Сервер матча не отвечает на ping, поэтому пинг сложен из двух замеров: " +
            Math.round(g.ping_first_mile_ms) + " мс от компьютера до VPN-сервера и " +
            Math.round(g.ping_node_ms) + " мс от VPN-сервера до региона игры. Меряется, пока идёт матч."
          : "Пинг до сервера матча через VPN: медиана трёх ICMP-замеров. Меряется, пока идёт матч.";
        const when = document.createElement("span");
        when.dataset.time = g.ping_at;
        when.dataset.tip = full_date(g.ping_at);
        when.textContent = ago(g.ping_at);
        sub.append(" · ", value, " · ", when);
      }
      cover.append(name, sub);
      // The whole face is one control: it boosts and starts the game, and
      // pressed again it stops the boost.
      // What a press does is written across the whole card under the
      // pointer: switch the boost on, or off.
      const act = document.createElement("span");
      act.className = "game-act";
      act.textContent = g.on ? "Выключить буст" : g.launchable ? "Играть" : "Включить буст";
      act.setAttribute("aria-hidden", "true");
      cover.append(act);
      if (g.on) cover.dataset.tip = "Игра пойдёт без буста. Антизапрет или VPN продолжат работать.";
      cover.addEventListener("click", () => g.on ? stop_boost() : boost(g.id, "", g.launchable));
      const face = document.createElement("div");
      face.className = "game-face";
      face.append(cover);

      if (g.regions && g.regions.length) {
        // Over the face, at the end of its bottom row.
        const regions = document.createElement("div");
        regions.className = "segment game-regions";
        regions.setAttribute("role", "radiogroup");
        regions.setAttribute("aria-label", "Регион серверов игры");
        for (const r of g.regions) {
          const b = document.createElement("button");
          b.setAttribute("role", "radio");
          const chosen = g.on && r.id === game_region;
          b.setAttribute("aria-checked", chosen ? "true" : "false");
          b.disabled = busy;
          b.textContent = r.name;
          b.dataset.tip = r.direct
            ? "Игра идёт напрямую, без VPN и его DNS: так она попадает на серверы " + r.name + "." + (chosen ? " Нажмите ещё раз, чтобы снова ускорять через VPN." : " Игра не запускается.")
            : chosen
            ? "Буст к серверам " + r.name + ". Нажмите ещё раз, чтобы ускорять к любому региону, где идёт игра."
            : "Ускорять к серверам " + r.name + ". Игра не запускается.";
          b.addEventListener("click", () => boost(g.id, chosen ? "" : r.id, false));
          regions.append(b);
        }
        face.append(regions);
      }
      content.append(face);
      if (g.server_offer) content.append(server_ask(g));
      if (expanded && g.detection_confirmed) {
        // How often the detected server was the one the game showed, among
        // the matches the player checked; absent until one was.
        const tally = document.createElement("div");
        tally.className = "game-tally";
        const words = document.createElement("span");
        words.textContent = "Сервер матча определён верно в " + g.detection_right + " из " + g.detection_confirmed;
        words.dataset.tip = "Сколько раз сервер, найденный приложением, совпал с тем, что показала игра, среди матчей, которые вы проверили. Пропущенные вопросы не считаются.";
        tally.append(words);
        content.append(tally);
      }
      box.append(card);
    }
  }

  // server_ask is the question under a card: is the match server found the
  // one the game itself shows? Where the game shows it is the hint.
  function server_ask(g) {
    const offer = g.server_offer;
    const ask_row = document.createElement("div");
    ask_row.className = "game-ask";
    const text = document.createElement("span");
    const server = document.createElement("span");
    server.className = "num";
    server.textContent = offer.server;
    server.dataset.tip = "Адрес, куда игра отправила больше всего пакетов за матч: " + offer.packets + " из " + offer.total +
      " (" + Math.round(100 * offer.packets / offer.total) + "%)" +
      (offer.runner_up ? "; следующий адрес — " + offer.runner_up : "") + ".";
    const question = document.createElement("span");
    question.textContent = "в игре тот же?";
    question.dataset.tip = offer.where;
    text.append("Сервер ", server, " — ", question);
    ask_row.append(text);

    const replying = server_reply && server_reply.game === g.id && server_reply.server === offer.server;
    const buttons = document.createElement("div");
    buttons.className = "game-ask-buttons";
    if (replying) {
      const field = document.createElement("input");
      field.type = "text";
      field.className = "game-ask-field";
      field.placeholder = "адрес из игры";
      field.value = server_reply.shown;
      field.setAttribute("aria-label", "Адрес сервера, который показывает игра");
      field.dataset.tip = "Необязательно. Адрес из окна сетевой статистики игры — с ним видно, какой сервер был на самом деле.";
      field.addEventListener("input", () => { server_reply.shown = field.value; });
      field.addEventListener("keydown", (e) => { if (e.key === "Enter") answer_server(g.id, offer.server, "no", field.value); });
      const send = document.createElement("button");
      send.className = "plain";
      send.textContent = "Готово";
      send.addEventListener("click", () => answer_server(g.id, offer.server, "no", field.value));
      buttons.append(field, send);
      setTimeout(() => field.focus(), 0);
    } else {
      const yes = document.createElement("button");
      yes.className = "plain";
      yes.textContent = "Да";
      yes.addEventListener("click", () => answer_server(g.id, offer.server, "yes", ""));
      const no = document.createElement("button");
      no.className = "plain";
      no.textContent = "Нет";
      no.addEventListener("click", () => { server_reply = { game: g.id, server: offer.server, shown: "" }; render_games(); });
      buttons.append(yes, no);
    }
    const dismiss = document.createElement("button");
    dismiss.className = "plain game-ask-dismiss";
    dismiss.textContent = "×";
    dismiss.setAttribute("aria-label", "Не отвечать");
    dismiss.dataset.tip = "Убрать вопрос. Ответ не засчитывается ни как верный, ни как неверный.";
    dismiss.addEventListener("click", () => answer_server(g.id, offer.server, "dismiss", ""));
    buttons.append(dismiss);
    ask_row.append(buttons);
    return ask_row;
  }

  async function answer_server(game, server, answer, shown) {
    const app = backend();
    if (!app || typeof app.Answer_match_server !== "function") return;
    server_reply = null;
    let outcome = null;
    try { outcome = await app.Answer_match_server(game, server, answer, (shown || "").trim()); } catch (e) { outcome = null; }
    // An offer that moved on meanwhile is read again rather than reported:
    // the card then asks about the server it now names.
    show_error("action-error", outcome && outcome.code !== "offer_changed" ? outcome : null);
    await load_games();
  }

  // boost puts the game alone through the server toward region, connects
  // if nothing is up, and starts the game when launch is set.
  async function boost(id, region, launch) {
    if (games === null || busy) return;
    const game = games.find((g) => g.id === id);
    const same = game && game.on && games.every((g) => g.on === (g.id === id)) && region === game_region;
    // Another game set or another region moves the tunnel, which ends a match.
    if (!same && in_match && !(await ask("Идёт матч. Переключить буст? Соединение перестроится, и матч прервётся.", "Переключить"))) return;
    close_pops();
    busy = boosting = true;
    show_error("action-error", null);
    render();
    let outcome = same ? {} : await backend().Set_games([id], region);
    const st = view && view.status ? view.status.state : "";
    const off = !(st === "connected" || st === "degraded" || st === "connecting" || st === "switching");
    // A region played from home needs no tunnel for the game: nothing is
    // connected for it.
    const direct = !!(game && (game.regions || []).find((r) => r.id === region && r.direct));
    if (!(outcome && outcome.code) && off && !direct) outcome = await backend().Connect();
    if (!(outcome && outcome.code) && launch) outcome = await backend().Launch_game(id);
    busy = boosting = false;
    // The record stands even when connecting failed; read back what the
    // service now holds rather than assuming.
    await load_games();
    show_error("action-error", outcome);
    await refresh();
  }

  // stop_boost turns the VPN off and forgets the game, so that the next
  // press of the power button is a plain VPN again.
  async function stop_boost() {
    if (busy) return;
    if (in_match && !(await ask("Идёт матч. Выключить буст? Матч прервётся.", "Выключить"))) return;
    busy = boosting = true;
    show_error("action-error", null);
    render();
    // The service rebuilds the tunnel for the chosen mode; nothing is
    // disconnected.
    const outcome = await backend().Set_games([], "");
    busy = boosting = false;
    await load_games();
    show_error("action-error", outcome);
    await refresh();
  }

  async function on_power() {
    if (!view || !view.status || busy) return;
    const st = view.status.state;
    const turning_off = st === "connected" || st === "degraded" || st === "connecting" || st === "switching";
    if (turning_off && strategy && strategy.anonymity === "high") {
      if (!(await ask("Отключить VPN? Трафик пойдёт напрямую.", "Отключить"))) return;
    }
    busy = true;
    show_error("action-error", null);
    render();
    const outcome = turning_off ? await backend().Disconnect() : await backend().Connect();
    busy = false;
    show_error("action-error", outcome);
    await refresh();
  }

  // --- the server menu ---------------------------------------------------
  // One menu holds the country and the server: a country unfolds into its
  // servers, its first line picks the country itself and leaves the server
  // to automatic choice, and any server below picks that server.

  // group_servers puts the servers under their countries, the countries in
  // the order of their Russian names; servers with no country come last.
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

  // group_ping is the country's best answer: the lowest of the fresh ones,
  // or else the lowest of the newest round any of its servers answered.
  function group_ping(group) {
    let best = null;
    for (const server of group.servers) {
      const p = server.available ? pings[server.node_id] : null;
      if (!p) continue;
      if (best === null || p.at > best.at + ping_keep_ms || (Math.abs(p.at - best.at) <= ping_keep_ms && p.rtt < best.rtt)) best = p;
    }
    return best;
  }

  const ping_tip = "Ping с этого компьютера напрямую, мимо туннеля; пока меню открыто, обновляется раз в 5 секунд.";

  // ping_into writes a ping as its number, followed by how long ago it was
  // taken once it is no longer this second's; the full date is the hint.
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

  // The last answer of every server is kept between runs: a number with the
  // moment it was measured is a measurement, however old.
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

  // live_node is the server the tunnel is on now, or "".
  function live_node() {
    const st = view && view.status;
    return st && st.session_id ? st.node_id || "" : "";
  }

  // auto_option is the automatic choice in the list, drawn as the same
  // badge the server plate wears when it is chosen automatically, after
  // what it chooses: a country or a server.
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

  function render_server_menu() {
    const list = $("server-list");
    if (servers === null) return;
    const scroll = $("servers").scrollTop;
    list.textContent = "";
    const s = strategy || {};
    const auto = !s.node_id;
    const strategy_code = flags.country_code(s.country);
    const live = live_node();

    list.append(auto_option("Страна", auto && !strategy_code, "Страна и сервер выбираются автоматически.", () => change({ country: "", node_id: "" })));

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

      // Under an open country its servers hang off a line drawn from the
      // flag; the stretch of it leading to the server connected to is lit.
      const rows = [];
      // The country itself can only be asked for by its code.
      if (group.code) {
        rows.push(auto_option("Сервер", auto && group.code === strategy_code, "Сервер в этой стране выбирается автоматически.", () => change({ country: group.code, node_id: "" })));
      }
      for (const server of group.servers) {
        const b = option(label_of(server), s.node_id === server.node_id, () => change({ node_id: server.node_id }));
        if (server.node_id === live) {
          b.classList.add("live");
          b.dataset.tip = "Подключено к этому серверу.";
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

  // update_pings rewrites only the numbers, so the menu does not rebuild
  // under the pointer at every ping.
  function update_pings() {
    for (const el of $("server-list").querySelectorAll("[data-node]")) {
      ping_into(el, pings[el.dataset.node]);
    }
    const groups = new Map(group_servers().map((g) => [g.key, g]));
    for (const el of $("server-list").querySelectorAll("[data-group]")) {
      const g = groups.get(el.dataset.group);
      ping_into(el, g ? group_ping(g) : null);
    }
  }

  async function ping_once() {
    if (ping_pending) return;
    ping_pending = true;
    let answer = null;
    try { answer = await backend().Ping_servers(); } catch (e) { answer = null; } finally { ping_pending = false; }
    if (!answer || answer.error) return;
    const now = Date.now();
    for (const p of answer.pings || []) {
      if (typeof p.rtt_ms === "number") pings[p.node_id] = { rtt: p.rtt_ms, at: now };
    }
    save_pings();
    if (!$("servers").hidden) update_pings();
  }

  // While the menu is open every server is pinged every five seconds; while
  // it is closed, once in a while, so the numbers it opens with are recent.
  function start_pings() {
    stop_pings();
    ping_once();
    ping_timer = setInterval(ping_once, 5000);
  }
  function stop_pings() {
    if (ping_timer) clearInterval(ping_timer);
    ping_timer = 0;
  }
  function background_ping() {
    if (ping_timer || !main_active || document.hidden) return;
    ping_once();
  }

  // place_menu opens the menu on whichever side of the chip has the room,
  // and no taller than that room.
  function place_menu() {
    const pop = $("servers");
    const chip = $("chip").getBoundingClientRect();
    const below = window.innerHeight - chip.bottom - 12;
    const above = chip.top - 12;
    const down = below >= 240 || below >= above;
    pop.classList.toggle("up", !down);
    pop.style.maxHeight = Math.max(160, Math.min(420, down ? below : above)) + "px";
  }

  async function open_servers() {
    if (!$("servers").hidden) { close_pops(); return; }
    close_pops();
    open_pop("servers", "chip");
    place_menu();
    const note = $("servers-note");
    if (servers === null) {
      set_text("server-list", "");
      note.hidden = false;
      note.textContent = "Загружаем список…";
    }
    const request = ++list_request;
    const answer = await load_servers();
    if (request !== list_request || $("servers").hidden) return;
    if (answer.error) {
      note.hidden = false;
      note.textContent = "Список не загрузился: " + (answer.error.message || answer.error.code);
      return;
    }
    note.hidden = servers.length > 0;
    note.textContent = "Серверов нет.";
    // Unfold where the choice is now: the picked server's country, the
    // country automatic choice keeps to, or the one connected to.
    const s = strategy || {};
    const picked = server_by_id(s.node_id);
    const here = picked ? picked.country : (s.country || (view && view.status && view.status.country) || "");
    const c = country_of(here);
    if (c.code || c.name) open_groups.add(c.code || c.name);
    const groups = group_servers();
    if (groups.length === 1) open_groups.add(groups[0].key);
    render_server_menu();
    start_pings();
  }

  // The switch after "Сервер": on means the control plane picks, which
  // also moves the connection off a server that dies. Turning it off keeps
  // the server the tunnel is on -- the service sees the same node and does
  // not reconnect; with no tunnel there is nothing to keep, so it opens the
  // menu to pick one.
  function open_levels() {
    if (!$("levels").hidden) { close_pops(); return; }
    close_pops();
    for (const b of $("levels").querySelectorAll("[data-level]")) {
      b.setAttribute("aria-selected", (strategy.anonymity || "") === b.dataset.level ? "true" : "false");
    }
    open_pop("levels", "goal-anonymity");
  }

  // --- pop-ups -----------------------------------------------------------

  function open_pop(id, owner) {
    hide_tip();
    $(id).hidden = false;
    $(owner).setAttribute("aria-expanded", "true");
  }

  function close_pop(id) {
    $(id).hidden = true;
    $(pop_owner[id]).setAttribute("aria-expanded", "false");
    if (id === "servers") stop_pings();
  }

  function close_pops() {
    for (const id of pops) close_pop(id);
  }

  // --- hints -------------------------------------------------------------
  // A hint belongs to the thing it explains: it shows on hovering or
  // focusing that thing, never as text printed somewhere apart from it.

  let tip_target = null;
  let tip_timer = 0;

  function show_tip(el) {
    const text = el.dataset.tip;
    if (!text || !el.isConnected) return;
    const tip = $("tip");
    tip.textContent = text;
    tip.hidden = false;
    const r = el.getBoundingClientRect();
    const t = tip.getBoundingClientRect();
    const left = Math.max(8, Math.min(r.left + r.width / 2 - t.width / 2, window.innerWidth - t.width - 8));
    let top = r.bottom + 8;
    if (top + t.height > window.innerHeight - 8) top = r.top - t.height - 8;
    tip.style.left = left + "px";
    tip.style.top = Math.max(8, top) + "px";
  }

  function hide_tip() {
    clearTimeout(tip_timer);
    tip_target = null;
    $("tip").hidden = true;
  }

  function tip_owner(node) {
    return node && node.closest ? node.closest("[data-tip]") : null;
  }

  function watch_tips() {
    document.addEventListener("pointerover", (e) => {
      const el = tip_owner(e.target);
      if (el === tip_target) return;
      hide_tip();
      if (!el) return;
      tip_target = el;
      tip_timer = setTimeout(() => show_tip(el), 350);
    });
    document.addEventListener("pointerout", (e) => { if (!e.relatedTarget) hide_tip(); });
    document.addEventListener("focusin", (e) => {
      const el = tip_owner(e.target);
      hide_tip();
      if (el && el.matches(":focus-visible")) { tip_target = el; show_tip(el); }
    });
    document.addEventListener("focusout", hide_tip);
    document.addEventListener("pointerdown", hide_tip);
    document.addEventListener("scroll", hide_tip, true);
  }

  // --- the detailed mode -------------------------------------------------
  // The detailed mode is the wide window: widening the window by hand opens
  // it, and the strip on the right widens or narrows the window.

  // The window is widened by the app, which keeps it on the screen; the
  // runtime's own calls measure position and size in different units.
  function set_width(width) {
    const app = window.go && window.go.main && window.go.main.App;
    if (!app || typeof app.Set_window_width !== "function") return false;
    app.Set_window_width(width);
    return true;
  }

  function toggle_expanded() {
    if (!set_width(expanded ? compact_width : expanded_width)) {
      // A page with no window to resize, such as a preview in a browser.
      expanded = !expanded;
      render();
    }
  }

  function restore_expanded() {
    if (load_expanded() && !wide_query.matches) set_width(expanded_width);
  }

  async function on_sign_in(event) {
    event.preventDefault();
    const submit = $("signin-submit");
    submit.disabled = true;
    show_error("signin-error", null);
    const password = $("password");
    const outcome = await backend().Sign_in($("username").value.trim(), password.value);
    // The password is not kept in the page either, whatever the outcome.
    password.value = "";
    submit.disabled = false;
    show_error("signin-error", outcome);
    strategy = null;
    tunnel = null;
    servers = null;
    await refresh();
  }

  async function on_sign_out() {
    if (!await ask("Выйти из аккаунта? Сейчас войти снова можно только зная пароль. Убедитесь, что вы его помните.", "Выйти")) return;
    close_pops();
    const outcome = await backend().Sign_out();
    show_error("action-error", outcome);
    strategy = null;
    tunnel = null;
    servers = null;
    await refresh();
  }

  async function toggle_menu() {
    const open = $("menu").hidden;
    close_pops();
    if (!open) return;
    show_level("menu", "menu-main");
    open_pop("menu", "menu-button");
    load_profiles();
  }

  // The build is the application's, not the account's: it is said in the
  // settings, and only what the build supplied.
  async function load_version() {
    const v = await backend().Version();
    const parts = [];
    if (v.version) parts.push("Версия " + v.version);
    if (v.commit) parts.push(v.commit);
    $("version").hidden = parts.length === 0;
    $("version").textContent = parts.join(" · ");
  }

  function start() {
    $("power").addEventListener("click", on_power);
    $("signin").addEventListener("submit", on_sign_in);
    $("sign-out").addEventListener("click", on_sign_out);
    $("menu-button").addEventListener("click", toggle_menu);
    window.addEventListener("account-plan-open", () => { close_pops(); open_pop("menu", "menu-button"); });
    $("help-button").addEventListener("click", () => {
      const opening = $("help").hidden;
      close_pops();
      if (opening) {
        open_pop("help", "help-button");
        window.help_ui.open();
      }
    });
    document.addEventListener("keydown", (event) => {
      if (event.key !== "Escape") return;
      const id = pops.find((name) => !$(name).hidden);
      if (id) $(pop_owner[id]).focus();
    }, true);

    $("chip").addEventListener("click", open_servers);
    $("gamer").addEventListener("click", toggle_gamer);
    $("compact-journal").addEventListener("click", toggle_journal);
    $("journal-title").addEventListener("click", () => { if (!expanded) toggle_journal(); });
    $("journal-retry").addEventListener("click", load_journal);
    $("settings-button").addEventListener("click", toggle_settings);
    $("theme-row").addEventListener("click", () => show_level("settings", "theme-panel"));
    $("theme-back").addEventListener("click", () => show_level("settings", "settings-main"));
    theme_editor.bind(theme_mode, () => theme_of(theme_mode()).id, () => {
      apply_theme();
      render_themes();
    }, show_level);
    $("statistics-row").addEventListener("click", () => show_level("settings", "statistics-panel"));
    $("statistics-back").addEventListener("click", () => show_level("settings", "settings-main"));
    $("about-row").addEventListener("click", () => { window.about_ui.render($("about-body"), ago); show_level("settings", "about-panel"); });
    $("about-back").addEventListener("click", () => show_level("settings", "settings-main"));
    $("blocks-line").addEventListener("click", () => {
      services_open = !services_open;
      try { localStorage.setItem("services.open", services_open ? "1" : "0"); } catch (e) {}
      if (services_open && !services_list) load_services(); else render_blocks();
    });
    $("services-edit").addEventListener("click", toggle_services_edit);
    $("services-restore").addEventListener("click", () => services_call(() => backend().Restore_services()));
    $("services-add").addEventListener("submit", async (event) => {
      event.preventDefault();
      const input = $("services-host"), host = input.value.trim();
      if (!host) { input.focus(); return; }
      if (await services_call(() => backend().Add_service(host))) input.value = "";
      input.focus();
    });
    $("blocks-refresh").addEventListener("click", refresh_analysis);
    $("profile-row").addEventListener("click", () => { show_level("menu", "profile-panel"); load_profiles(); });
    $("profile-back").addEventListener("click", () => show_level("menu", "menu-main"));
    // The goal tiles are out of the window until they work.
    // $("goal-bypass").addEventListener("click", () => change({ bypass: !strategy.bypass }));
    // $("goal-route").addEventListener("click", () => change({ route: !strategy.route }));
    // $("goal-anonymity").addEventListener("click", open_levels);
    for (const b of $("mode").querySelectorAll("[data-mode]")) {
      b.addEventListener("click", () => change_tunnel(b.dataset.mode));
    }
    // for (const b of $("levels").querySelectorAll("[data-level]")) {
    //   b.addEventListener("click", () => change({ anonymity: b.dataset.level }));
    // }
    $("edge").addEventListener("click", toggle_expanded);
    wide_query.addEventListener("change", () => {
      expanded = wide_query.matches;
      save_expanded();
      close_pops();
      render();
    });
    window.addEventListener("resize", () => { if (!$("servers").hidden) place_menu(); });
    // The path is taken when the click happens: a country row rebuilds the
    // list under the pointer, and its button is out of the page by the time
    // the click reaches here, so contains() alone would call it outside.
    document.addEventListener("click", (e) => {
      const path = e.composedPath();
      for (const id of pops) {
        if (!$(id).hidden && !path.includes($(id)) && !path.includes($(pop_owner[id]))) {
          close_pop(id);
        }
      }
    });
    document.addEventListener("keydown", (e) => { if (e.key === "Escape") { close_pops(); hide_tip(); } });
    watch_tips();
    window.runtime.EventsOn("changed", () => { refresh(); reread_analysis(); });
    // Events cover state changes; the poll picks up new measurements, which
    // the service takes on its own schedule without announcing them.
    setInterval(refresh, 3000);
    setInterval(tick_times, 1000);
    load_pings();
    setInterval(background_ping, 60000);
    restore_expanded();
    const app = backend();
    if (app && typeof app.Stick_to_edges === "function") app.Stick_to_edges();
    load_gamer().then(() => { if (view) render(); });
    refresh();
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start);
  else start();
})();
