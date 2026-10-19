// The page renders what the service reports and nothing else. Every number
// shown is a field the service sent; a field that is absent is not drawn,
// never replaced by a zero or a dash.
//
// Each thing on the screen is set in one place, next to itself: automatic
// server choice sits right after the word "Сервер", the anonymity level
// lives inside its own tile, and how a goal was applied is shown on that
// goal's tile, not repeated in a panel of its own.
(function () {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const backend = () => window.go.main.App;

  const screens = ["screen-wait", "screen-service", "screen-untrusted", "screen-signin", "screen-main"];
  const pops = ["menu", "servers", "countries", "levels"];
  const state_text = {
    disconnected: "Не подключено",
    connecting: "Подключение…",
    connected: "Подключено",
    degraded: "Связь ухудшилась — проверяем",
    switching: "Переключаемся на другой сервер…",
    disconnecting: "Отключение…",
    failed: "Не удалось подключиться"
  };
  const level_text = { "": "Выкл", light: "Лёгкая", standard: "Средняя", high: "Высокая" };
  const reason_text = {
    least_loaded: "выбран наименее загруженный",
    chosen_node: "выбран вами",
    lowest_rtt: "самый быстрый канал"
  };
  const why_text = {
    not_supported: "серверы пока этого не дают",
    no_reach_evidence: "о вашей сети пока нет данных",
    chosen_node: "сервер выбран вручную",
    no_client: "ещё нет профиля для замеров",
    not_enough_measurements: "мало замеров, чтобы сравнить"
  };

  let view = null;
  let strategy = null; // as the service last reported it; null until read
  let servers = null;  // the last server list; null until fetched
  let busy = false;
  let loading_strategy = false;
  let expanded = load_expanded();
  const region_names = make_region_names();

  function make_region_names() {
    try { return new Intl.DisplayNames(["ru"], { type: "region" }); } catch (e) { return null; }
  }
  function country_name(code) {
    if (!code) return "";
    try { return (region_names && region_names.of(code)) || code; } catch (e) { return code; }
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
    $("corner").hidden = !main;
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

  function place_of(country, city, name) {
    const c = country_name(country);
    if (c && city) return c + " · " + city;
    return c || name || "";
  }

  function server_by_id(id) {
    return (servers || []).find((s) => s.node_id === id) || null;
  }

  function local_time(iso) {
    if (!iso) return "";
    const d = new Date(iso);
    if (isNaN(d.getTime())) return "";
    return d.toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit", second: "2-digit" });
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
    if (strategy === null) load_strategy();

    const st = status.state;
    const up = st === "connected" || st === "degraded";
    const moving = st === "connecting" || st === "switching" || st === "disconnecting";
    const s = strategy || {};
    const auto = !s.node_id;

    // Server: what it is connected to; otherwise what was picked; otherwise
    // that the choice is automatic.
    const auto_switch = $("auto");
    auto_switch.setAttribute("aria-checked", auto ? "true" : "false");
    let where = status.session_id ? place_of(status.country, status.city, status.node_name || status.node_id) : "";
    if (!where && !auto) {
      const picked = server_by_id(s.node_id);
      where = picked ? (picked.name || place_of(picked.country, picked.city, "")) : s.node_id;
    }
    set_text("place", where || "Выбирается автоматически");
    $("rtt-line").hidden = !(up && status.rtt_ms !== undefined);
    set_text("rtt", ms(status.rtt_ms));
    const reason = status.session_id && status.reason ? reason_text[status.reason] || "" : "";
    $("reason").hidden = !(expanded && reason);
    set_text("reason", reason);

    const note = $("note");
    note.className = "note" + (st === "connected" ? " ok" : (st === "degraded" || st === "failed" ? " warn" : ""));
    set_text("note-text", state_text[st] || st);

    const power = $("power");
    power.className = "power" + (up && st === "connected" ? " on" : "") + (moving ? " busy" : "") + (st === "degraded" || st === "failed" ? " trouble" : "");
    const offers_off = up || st === "connecting" || st === "switching";
    set_text("power-label", offers_off ? (up ? "Подключено" : "Подключение…") : (st === "disconnecting" ? "Отключение…" : "Подключиться"));
    power.setAttribute("aria-label", offers_off ? "Отключить VPN" : "Подключить VPN");
    power.disabled = busy || st === "disconnecting";

    const has_session = !!status.session_id;
    $("details").hidden = !(expanded && has_session);
    row("node", status.node_name || status.node_id);
    row("protocol", status.protocol);
    row("since", local_time(status.since));
    row("rtt", ms(status.rtt_ms));
    row("loss", status.loss_percent === undefined ? "" : Math.round(status.loss_percent) + "%");
    row("measured", local_time(status.measured_at));

    render_goals(status, s);

    $("window").classList.toggle("expanded", expanded);
    const corner = $("corner");
    corner.setAttribute("aria-pressed", expanded ? "true" : "false");
    corner.setAttribute("aria-label", expanded ? "Простой режим" : "Подробный режим");
  }

  function render_goals(status, s) {
    const known = strategy !== null;
    for (const id of ["goal-bypass", "goal-geo", "goal-route", "goal-anonymity"]) $(id).disabled = !known || busy;
    $("goal-bypass").setAttribute("aria-pressed", s.bypass ? "true" : "false");
    $("goal-route").setAttribute("aria-pressed", s.route ? "true" : "false");
    $("goal-geo").classList.toggle("on", !!s.country);
    $("goal-anonymity").classList.toggle("on", !!s.anonymity);
    set_text("geo-value", s.country ? country_name(s.country) : "любая");
    set_text("anon-value", level_text[s.anonymity || ""]);

    // How each goal fared is shown on the goal itself, in the detailed
    // mode -- except that an anonymity level which does nothing yet says so
    // in both modes: a level that only looks like protection must not be
    // mistaken for it.
    const subs = { bypass: s.bypass, geo: !!s.country, route: s.route, anonymity: !!s.anonymity };
    for (const goal of Object.keys(subs)) {
      let text = subs[goal] && expanded ? outcome_text(goal, goal_outcome(status, goal)) : "";
      const applied = goal_outcome(status, goal);
      if (goal === "anonymity" && s.anonymity && !(applied && applied.applied)) text = "пока не действует";
      const el = $("sub-" + goal);
      el.hidden = !text;
      el.textContent = text;
      el.classList.toggle("ok", !!(applied && applied.applied));
    }
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
    try { answer = await backend().Strategy(); } finally { loading_strategy = false; }
    if (answer && answer.strategy) {
      strategy = answer.strategy;
      if (strategy.node_id && servers === null) load_servers();
      render();
    }
  }

  async function load_servers() {
    const answer = await backend().Servers();
    if (answer && !answer.error) servers = answer.servers || [];
    return answer;
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
      if (!(await ask("Высокая анонимность будет снята.", "Снять"))) return;
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

  async function on_power() {
    if (!view || !view.status || busy) return;
    const st = view.status.state;
    const turning_off = st === "connected" || st === "degraded" || st === "connecting" || st === "switching";
    if (turning_off && strategy && strategy.anonymity === "high") {
      if (!(await ask("Защита пропадёт: трафик пойдёт напрямую, мимо VPN.", "Отключить"))) return;
    }
    busy = true;
    show_error("action-error", null);
    render();
    const outcome = turning_off ? await backend().Disconnect() : await backend().Connect();
    busy = false;
    show_error("action-error", outcome);
    await refresh();
  }

  // --- the server list -------------------------------------------------

  async function open_servers() {
    if (!$("servers").hidden) { close_pops(); return; }
    close_pops();
    open_pop("servers", "chip");
    set_text("server-list", "");
    const note = $("servers-note");
    note.hidden = false;
    note.textContent = "Загружаем список…";
    const answer = await load_servers();
    if (answer.error) {
      note.textContent = "Список не загрузился: " + (answer.error.message || answer.error.code);
      return;
    }
    note.hidden = servers.length > 0;
    note.textContent = "Серверов нет.";
    const list = $("server-list");
    for (const server of servers) {
      const b = document.createElement("button");
      b.className = "opt server-opt";
      b.setAttribute("role", "option");
      b.disabled = !server.available;
      const picked = strategy && strategy.node_id === server.node_id;
      b.setAttribute("aria-selected", picked ? "true" : "false");
      const name = document.createElement("span");
      name.textContent = server.name || place_of(server.country, server.city, server.node_id);
      const sub = document.createElement("span");
      sub.className = "opt-sub";
      sub.textContent = [server.name ? place_of(server.country, server.city, "") : "", server.available ? "" : "недоступен"].filter(Boolean).join(" · ");
      b.append(name, sub);
      b.addEventListener("click", () => change({ node_id: server.node_id }));
      list.append(b);
    }
    render();
  }

  // The switch after "Сервер": on means the control plane picks, which
  // also moves the connection off a server that dies. Turning it off is
  // picking a server, so it opens the list.
  function on_auto() {
    if (strategy === null || busy) return;
    if (strategy.node_id) change({ node_id: "" });
    else open_servers();
  }

  // --- goals -------------------------------------------------------------

  async function open_countries() {
    if (!$("countries").hidden) { close_pops(); return; }
    close_pops();
    open_pop("countries", "goal-geo");
    const list = $("country-list");
    list.textContent = "";
    const note = $("countries-note");
    note.hidden = false;
    note.textContent = "Загружаем…";
    const answer = await load_servers();
    if (answer.error) {
      note.textContent = "Список не загрузился: " + (answer.error.message || answer.error.code);
      return;
    }
    note.hidden = true;
    // Only countries some server is in: a country with none cannot be
    // asked for and would only produce a refusal.
    const countries = new Map();
    for (const server of servers) {
      if (!server.country) continue;
      countries.set(server.country, (countries.get(server.country) || false) || server.available);
    }
    const options = [["", "Любая", true]].concat(
      Array.from(countries.entries())
        .map(([code, available]) => [code, country_name(code), available])
        .sort((a, b) => a[1].localeCompare(b[1], "ru")));
    for (const [code, label, available] of options) {
      const b = document.createElement("button");
      b.className = "opt";
      b.setAttribute("role", "option");
      b.setAttribute("aria-selected", (strategy.country || "") === code ? "true" : "false");
      b.textContent = label;
      if (!available) {
        const sub = document.createElement("span");
        sub.className = "opt-sub";
        sub.textContent = "сейчас недоступна";
        b.append(sub);
      }
      b.addEventListener("click", () => change({ country: code }));
      list.append(b);
    }
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

  const pop_owner = { menu: "menu-button", servers: "chip", countries: "goal-geo", levels: "goal-anonymity" };

  function open_pop(id, owner) {
    $(id).hidden = false;
    $(owner).setAttribute("aria-expanded", "true");
  }

  function close_pops() {
    for (const id of pops) {
      $(id).hidden = true;
      $(pop_owner[id]).setAttribute("aria-expanded", "false");
    }
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
    servers = null;
    await refresh();
  }

  async function on_sign_out() {
    close_pops();
    const outcome = await backend().Sign_out();
    show_error("action-error", outcome);
    strategy = null;
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
    $("goal-geo").addEventListener("click", open_countries);
    $("goal-anonymity").addEventListener("click", open_levels);
    for (const b of $("levels").querySelectorAll("[data-level]")) {
      b.addEventListener("click", () => change({ anonymity: b.dataset.level }));
    }
    $("corner").addEventListener("click", () => { expanded = !expanded; save_expanded(); render(); });
    document.addEventListener("click", (e) => {
      for (const id of pops) {
        if (!$(id).hidden && !$(id).contains(e.target) && !$(pop_owner[id]).contains(e.target) && !($(id) === $("servers") && $("auto").contains(e.target))) {
          $(id).hidden = true;
          $(pop_owner[id]).setAttribute("aria-expanded", "false");
        }
      }
    });
    document.addEventListener("keydown", (e) => { if (e.key === "Escape") close_pops(); });
    window.runtime.EventsOn("changed", refresh);
    // Events cover state changes; the poll picks up new measurements, which
    // the service takes on its own schedule without announcing them.
    setInterval(refresh, 3000);
    refresh();
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start);
  else start();
})();
