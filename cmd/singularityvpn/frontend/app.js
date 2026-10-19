// The page renders what the service reports and nothing else. Every number
// shown is a field the service sent; a field that is absent is not drawn,
// never replaced by a zero or a dash.
(function () {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const backend = () => window.go.main.App;

  const screens = ["screen-wait", "screen-service", "screen-untrusted", "screen-signin", "screen-main"];
  const state_text = {
    disconnected: "Не подключено",
    connecting: "Подключение…",
    connected: "Подключено",
    degraded: "Связь ухудшилась — проверяем",
    switching: "Переключаемся на другой сервер…",
    disconnecting: "Отключение…",
    failed: "Не удалось подключиться"
  };

  let view = null;
  let busy = false;
  let expanded = load_expanded();

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
    if ($("menu-button").hidden) close_menu();
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

  function place(status) {
    if (status.country && status.city) return status.country + " · " + status.city;
    return status.country || status.node_name || status.node_id || "";
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

  function render() {
    if (!view) { show_screen("screen-wait"); return; }
    if (view.error && view.error.code === "untrusted_pipe") { show_screen("screen-untrusted"); return; }
    if (!view.service_running) { show_screen("screen-service"); return; }
    const status = view.status;
    if (!status) { show_screen("screen-wait"); return; }
    if (!status.signed_in) { show_screen("screen-signin"); return; }
    show_screen("screen-main");

    const st = status.state;
    const up = st === "connected" || st === "degraded";
    const moving = st === "connecting" || st === "switching" || st === "disconnecting";
    const where = place(status);

    $("chip").hidden = !where;
    set_text("place", where);
    $("rtt-line").hidden = !(up && status.rtt_ms !== undefined);
    set_text("rtt", ms(status.rtt_ms));

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

    const corner = $("corner");
    corner.setAttribute("aria-pressed", expanded ? "true" : "false");
    corner.setAttribute("aria-label", expanded ? "Простой режим" : "Подробный режим");
  }

  async function refresh() {
    try {
      view = await backend().Status();
    } catch (e) {
      view = { service_running: false };
    }
    render();
  }

  function show_error(id, outcome) {
    const el = $(id);
    if (!outcome || !outcome.code) { el.hidden = true; return; }
    const known = {
      unauthorized: "Неверное имя пользователя или пароль.",
      busy: "Служба занята предыдущей командой. Попробуйте через несколько секунд.",
      not_signed_in: "Нужно войти заново.",
      service_not_running: "Служба не запущена.",
      untrusted_pipe: "Канал к службе не прошёл проверку."
    };
    el.textContent = known[outcome.code] || ("Не получилось: " + (outcome.message || outcome.code));
    el.hidden = false;
  }

  async function on_power() {
    if (!view || !view.status || busy) return;
    const st = view.status.state;
    const turning_off = st === "connected" || st === "degraded" || st === "connecting" || st === "switching";
    busy = true;
    show_error("action-error", null);
    render();
    const outcome = turning_off ? await backend().Disconnect() : await backend().Connect();
    busy = false;
    show_error("action-error", outcome);
    await refresh();
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
    await refresh();
  }

  async function on_sign_out() {
    close_menu();
    const outcome = await backend().Sign_out();
    show_error("action-error", outcome);
    await refresh();
  }

  function close_menu() {
    $("menu").hidden = true;
    $("menu-button").setAttribute("aria-expanded", "false");
  }

  async function toggle_menu() {
    const open = $("menu").hidden;
    $("menu").hidden = !open;
    $("menu-button").setAttribute("aria-expanded", open ? "true" : "false");
    if (open) {
      const v = await backend().Version();
      const parts = [];
      if (v.version) parts.push("Версия " + v.version);
      if (v.commit) parts.push(v.commit);
      $("version").hidden = parts.length === 0;
      $("version").textContent = parts.join(" · ");
    }
  }

  function start() {
    $("power").addEventListener("click", on_power);
    $("signin").addEventListener("submit", on_sign_in);
    $("sign-out").addEventListener("click", on_sign_out);
    $("menu-button").addEventListener("click", toggle_menu);
    $("corner").addEventListener("click", () => { expanded = !expanded; save_expanded(); render(); });
    document.addEventListener("click", (e) => {
      if (!$("menu").hidden && !$("menu").contains(e.target) && !$("menu-button").contains(e.target)) close_menu();
    });
    window.runtime.EventsOn("changed", refresh);
    // Events cover state changes; the poll picks up new measurements, which
    // the service takes on its own schedule without announcing them.
    setInterval(refresh, 3000);
    refresh();
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start);
  else start();
})();
