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
  const pops = ["menu", "servers", "levels"];
  const pop_owner = { menu: "menu-button", servers: "chip", levels: "goal-anonymity" };
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
    degraded: "Связь ухудшилась — проверяем",
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
  const analysis_max_age_ms = 6 * 60 * 60 * 1000;
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
  let loading_journal = false;
  let games = null;     // the game cards as the service last reported them; null until read
  let loading_games = false;
  let games_unsupported = false;
  let games_read_at = 0; // when the game list was last read, for its pings
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
    for (const s of screens) $(s).hidden = s !== id;
    const main = id === "screen-main";
    if (!main) main_active = false;
    $("edge").hidden = !main;
    $("menu-button").hidden = !(main || id === "screen-signin") || !(view && view.status && view.status.signed_in);
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

    // Until the strategy is known nothing claims to be automatic or not:
    // drawing a default would state something the service never said.
    const auto_switch = $("auto");
    auto_switch.hidden = strategy === null;
    auto_switch.setAttribute("aria-checked", auto ? "true" : "false");
    document.querySelector(".goals").hidden = strategy === null;
    render_chip(status, s, auto, up);

    const power = $("power");
    power.className = "power" + (st === "connected" ? " on" : "") + (moving ? " busy" : "") + (st === "degraded" || st === "failed" ? " trouble" : "");
    set_text("power-label", power_text[st] || st);
    const offers_off = up || st === "connecting" || st === "switching";
    power.setAttribute("aria-label", offers_off ? "Отключить VPN" : "Подключить VPN");
    power.disabled = busy || st === "disconnecting";
    const trouble = trouble_text[st] || "";
    $("trouble").hidden = !trouble;
    set_text("trouble", trouble);

    // The information plate sits under the button, in the detailed mode.
    // Which server and how fast are on the server chip and not repeated here.
    const has_session = !!status.session_id;
    $("details").hidden = !(expanded && has_session);
    row_time("since", status.since);
    row("loss", status.loss_percent === undefined ? "" : Math.round(status.loss_percent) + "%");
    $("row-loss").querySelector(".l").dataset.tip = "Доля неудачных из четырёх TCP-соединений " +
      (status.tunnel === "antizapret" ? "с DNS сервера" : "с внешним адресом") + " через туннель, а не потери пакетов.";
    row_time("measured", status.measured_at);

    render_mode(status, s);
    render_goals(status, s);
    render_games();
    render_blocks();
    if (expanded && !journal_unsupported) load_journal();
    render_journal();

    $("window").classList.toggle("expanded", expanded);
    const edge = $("edge");
    edge.setAttribute("aria-pressed", expanded ? "true" : "false");
    edge.setAttribute("aria-label", expanded ? "Простой режим" : "Подробный режим");
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
        title = "Любая страна";
      }
      sub.push("любой сервер");
    }
    $("chip").hidden = !title;
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

  // render_blocks draws the blocks line from the last analysis. Light mode
  // shows it only when something was found; the detailed mode also says when
  // nothing was, and opens one row per target under the line. With no
  // analysis -- or no targets in it -- nothing is drawn, never a placeholder.
  function render_blocks() {
    const el = $("blocks");
    const checks = analysis && analysis.checks ? analysis.checks : [];
    if (checks.length === 0) { el.hidden = true; return; }
    const blocked = checks.filter((c) => c.verdict === "blocked");
    let text = "";
    if (blocked.length > 0) text = "Блокируются: " + blocked.map((c) => c.label || c.host).join(", ");
    else if (analysis.control === "failed") text = "Сайты внутри страны не отвечают — похоже, нет сети или сбой у провайдера. VPN тут не поможет.";
    else if (expanded) text = "Блокировок не найдено";
    el.hidden = !text;
    if (!text) return;
    const line = set_text("blocks-line", text);
    line.classList.toggle("clear", blocked.length === 0 && analysis.control !== "failed");
    let hint = "Проверено напрямую, без VPN: DNS, соединение и TLS-рукопожатие. Не проверялось: обрыв после 16–20 КБ, белые списки подсетей.";
    if (analysis.control === "absent") hint += " Без контрольных сайтов внутри страны отказ нельзя уверенно назвать блокировкой.";
    line.dataset.tip = hint;

    $("blocks-details").hidden = !expanded;
    if (!expanded) return;
    const rows = $("blocks-rows");
    rows.textContent = "";
    for (const c of checks) {
      const r = document.createElement("div");
      r.className = "row";
      const l = document.createElement("span");
      l.className = "l";
      l.textContent = c.label || c.host;
      const v = document.createElement("span");
      v.textContent = c.verdict === "blocked" ? (method_text[c.method] || "заблокирован") : (verdict_text[c.verdict] || "");
      if (c.verdict !== "open") v.className = "bad";
      r.append(l, v);
      rows.append(r);
    }
    row_time("blocks-measured", analysis.measured_at);
  }

  // load_analysis runs when the main screen comes up: it reads what the
  // service holds and, if there is nothing or it is old and no connection
  // is up, asks for one measurement in the background. Any error leaves the
  // line hidden. A newer call makes an older answer irrelevant.
  async function load_analysis() {
    const request = ++analysis_request;
    let answer;
    try { answer = await backend().Analysis(); } catch (e) { answer = { error: { code: "failed" } }; }
    if (request !== analysis_request) return;
    analysis = answer && !answer.error && answer.result ? answer.result : null;
    render();
    if (answer && answer.error) return;

    const measured = analysis ? new Date(analysis.measured_at).getTime() : NaN;
    const fresh = !isNaN(measured) && Date.now() - measured < analysis_max_age_ms;
    if (fresh || !view || !view.status || view.status.state !== "disconnected") return;
    try { answer = await backend().Analyze(); } catch (e) { answer = { error: { code: "failed" } }; }
    if (request !== analysis_request) return;
    analysis = answer && !answer.error && answer.result ? answer.result : null;
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
    if (!isNaN(shown) && incoming <= shown) return;
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
      $("confirm").hidden = false;
      const done = (answer) => {
        $("confirm").hidden = true;
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
    if (live === "games") {
      note = "Сейчас только игры";
      tip = "Выбраны игры: через сервер идут только они, всё остальное — напрямую.";
    } else if (live && live !== tunnel) {
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
    let answer = null;
    try { answer = await app.Journal(); } catch (e) { answer = null; } finally { loading_journal = false; }
    if (answer && answer.error && answer.error.code === "unknown_command") { journal_unsupported = true; return; }
    if (answer && !answer.error) {
      journal_entries = answer.entries || [];
      if (servers === null && journal_entries.some((e) => e.attrs && e.attrs.node_id)) load_servers().then(render_journal);
      render_journal();
    }
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

  const tunnel_text = { vpn: "весь трафик", antizapret: "Антизапрет", games: "только игры" };

  const event_text = {
    connection_established: ["Подключено", "ok"],
    connection_lost: ["Связь с сервером потеряна", "warn"],
    connection_degraded: ["Связь ухудшилась", "warn"],
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

  function render_journal() {
    const box = $("journal");
    const show = expanded && journal_entries !== null && journal_entries.length > 0;
    box.hidden = !show;
    if (!show) return;
    const list = $("journal-list");
    const key = journal_entries.length + ":" + (journal_entries[journal_entries.length - 1] || {}).at + ":" + (servers ? servers.length : -1);
    if (list.dataset.key === key) return;
    list.dataset.key = key;
    list.textContent = "";
    for (let i = journal_entries.length - 1; i >= 0; i--) {
      const e = journal_entries[i];
      const [text, tone, tip] = journal_line(e);
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
  // One toggle tile per game in the catalog. An on game goes through the
  // server and the rest of the machine does not -- unless the game cannot be
  // carried on its own, which its tile says while it is on.

  async function load_games() {
    if (loading_games || games_unsupported) return;
    const app = backend();
    if (!app || typeof app.Games !== "function") { games_unsupported = true; return; }
    loading_games = true;
    let answer = null;
    try { answer = await app.Games(); } catch (e) { answer = null; } finally { loading_games = false; }
    if (answer && answer.error && answer.error.code === "unknown_command") games_unsupported = true;
    if (answer && !answer.error) { games = answer.games || []; games_read_at = Date.now(); render(); }
  }

  function render_games() {
    $("games").hidden = !(games && games.length);
    if (!games || !games.length) return;
    const box = $("game-cards");
    // The tiles are rebuilt only when the list changes, so one under the
    // pointer or the keyboard keeps its hover and its focus.
    const key = games.map((g) => g.id + (g.on ? "+" : "-") + (g.ping_at || "")).join(",") + (busy ? "!" : "");
    if (box.dataset.key === key) return;
    box.dataset.key = key;
    box.textContent = "";
    for (const g of games) {
      const b = document.createElement("button");
      b.className = "tile game";
      b.setAttribute("aria-pressed", g.on ? "true" : "false");
      b.disabled = busy;
      const name = document.createElement("span");
      name.className = "tile-name";
      name.textContent = g.name;
      b.append(name);
      if (g.on && g.whole_machine) {
        const sub = document.createElement("span");
        sub.className = "tile-sub";
        sub.textContent = "весь компьютер через VPN";
        sub.dataset.tip = g.touchy
          ? "Античит этой игры реагирует на сетевой драйвер, которым игры отделяются от остального трафика. Пока она включена, драйвера нет, а через сервер идёт весь компьютер."
          : "Для этой игры пока не известно, какие программы отделять. Пока она включена, через сервер идёт весь компьютер.";
        b.append(sub);
      }
      if (g.ping_ms != null && g.ping_at) {
        // The game's own ping: to the server it was playing on, through
        // the VPN server, measured while a match was on.
        const sub = document.createElement("span");
        sub.className = "tile-sub";
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
        sub.append(value, " · ", when);
        b.append(sub);
      }
      b.addEventListener("click", () => toggle_game(g.id));
      box.append(b);
    }
  }

  async function toggle_game(id) {
    if (games === null || busy) return;
    const ids = games.filter((g) => g.on !== (g.id === id)).map((g) => g.id);
    close_pops();
    busy = true;
    show_error("action-error", null);
    render();
    const outcome = await backend().Set_games(ids);
    busy = false;
    // The record stands even when rebuilding the tunnel failed; read back
    // what the service now holds rather than assuming.
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

  const ping_tip = "Ping с этого компьютера напрямую, мимо туннеля; пока меню открыто, обновляется каждую секунду.";

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

    list.append(option("Любая страна", auto && !strategy_code, () => change({ country: "", node_id: "" })));

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
        rows.push(option("Любой сервер", auto && group.code === strategy_code, () => change({ country: group.code, node_id: "" })));
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
  // under the pointer once a second.
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

  // While the menu is open every server is pinged each second; while it is
  // closed, once in a while, so the numbers it opens with are recent.
  function start_pings() {
    stop_pings();
    ping_once();
    ping_timer = setInterval(ping_once, 1000);
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
  function on_auto() {
    if (strategy === null || busy) return;
    if (strategy.node_id) { change({ node_id: "" }); return; }
    const live = live_node();
    if (live) { close_pops(); change({ node_id: live }); }
    else open_servers();
  }

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
    open_pop("menu", "menu-button");
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
    $("chip").addEventListener("click", open_servers);
    $("auto").addEventListener("click", on_auto);
    $("goal-bypass").addEventListener("click", () => change({ bypass: !strategy.bypass }));
    $("goal-route").addEventListener("click", () => change({ route: !strategy.route }));
    $("goal-anonymity").addEventListener("click", open_levels);
    for (const b of $("mode").querySelectorAll("[data-mode]")) {
      b.addEventListener("click", () => change_tunnel(b.dataset.mode));
    }
    for (const b of $("levels").querySelectorAll("[data-level]")) {
      b.addEventListener("click", () => change({ anonymity: b.dataset.level }));
    }
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
        if (!$(id).hidden && !path.includes($(id)) && !path.includes($(pop_owner[id])) && !($(id) === $("servers") && path.includes($("auto")))) {
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
    refresh();
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start);
  else start();
})();
