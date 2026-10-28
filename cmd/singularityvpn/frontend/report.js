// What the window says when it offers to report a problem: the message
// written in advance from what the service counted, and the list of what
// goes with it. Both are plain functions of their input, so they are tested
// without a window.
(function (root) {
  'use strict';

  function plural(n, one, few, many) {
    const m10 = n % 10, m100 = n % 100;
    if (m10 === 1 && m100 !== 11) return one;
    if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return few;
    return many;
  }
  const count = (n, one, few, many) => n + ' ' + plural(n, one, few, many);

  // message is the problem as the person would tell it, from what the
  // service counted: how many failures, of which kind, over how long, and
  // where. What they were doing is theirs to add.
  function message(failures, place) {
    if (!failures || !failures.count) return '';
    const kinds = [];
    if (failures.blips) kinds.push(count(failures.blips, 'короткий провал', 'коротких провала', 'коротких провалов'));
    if (failures.lost) kinds.push(count(failures.lost, 'потеря связи', 'потери связи', 'потерь связи'));
    if (failures.failed) kinds.push(count(failures.failed, 'неудачная проверка', 'неудачные проверки', 'неудачных проверок'));
    const seconds = Math.max(1, Math.round((Date.parse(failures.last) - Date.parse(failures.first)) / 1000));
    const span = seconds < 60 ? count(seconds, 'секунду', 'секунды', 'секунд') : count(Math.round(seconds / 60), 'минуту', 'минуты', 'минут');
    let text = 'Связь сбоит: ' + count(failures.count, 'сбой', 'сбоя', 'сбоев') + ' за ' + span +
      (kinds.length ? ' (' + kinds.join(', ') + ')' : '') + ', сеть при этом не менялась.';
    if (place) text += ' ' + place + '.';
    return text;
  }

  // windows_name reads a version such as 10.0.26300 the way Windows names
  // itself: from build 22000 on it is Windows 11, though the version still
  // says 10.
  function windows_name(os) {
    const m = /^(\d+)\.(\d+)\.(\d+)/.exec(os || '');
    if (!m) return os ? 'Windows ' + os : '';
    const build = Number(m[3]);
    return 'Windows ' + (m[1] === '10' && build >= 22000 ? '11' : m[1]) + ', сборка ' + build;
  }

  function kb(bytes) {
    return bytes < 1024 ? bytes + ' Б' : Math.max(1, Math.round(bytes / 1024)) + ' КБ';
  }

  // over is ' за <span>' from from to to, rounded to minutes under an hour and
  // to hours past it, or '' when either end is not known: what goes is said
  // over the time it really covers. After an earlier report that is only
  // what came since, not the whole window.
  function over(from, to) {
    const ms = Date.parse(to) - Date.parse(from);
    if (!(ms >= 0)) return '';
    const minutes = Math.max(1, Math.round(ms / 60000));
    if (minutes < 60) return ' за ' + count(minutes, 'минуту', 'минуты', 'минут');
    return ' за ' + count(Math.round(minutes / 60), 'час', 'часа', 'часов');
  }

  // attached is what goes with the message, one [title, detail] per part,
  // read from the diagnostics the service built -- the same object that is
  // sent, so the list cannot promise less than goes.
  function attached(diagnostics, log_bytes, log_from) {
    const d = diagnostics || {};
    const rows = [];
    const c = d.connection || {};
    if (c.node_id || c.protocol) rows.push(['Соединение', 'сервер, протокол, режим и последние замеры']);
    if (d.failures && d.failures.count) rows.push(['Сбои', count(d.failures.count, 'сбой', 'сбоя', 'сбоев') + ' за полчаса']);
    if (Array.isArray(d.journal) && d.journal.length) rows.push(['Журнал', count(d.journal.length, 'запись', 'записи', 'записей') + over(d.journal[0].at, d.created_at)]);
    if (Array.isArray(d.network) && d.network.length) {
      rows.push(['Сеть', d.network.map((a) => a.name).filter(Boolean).join(', ') + ' — тип, MTU, DNS, без адресов компьютера']);
    }
    if (Array.isArray(d.apps) && d.apps.length) {
      rows.push(['Программы на экране', [...new Set(d.apps.map((a) => a.exe).filter(Boolean))].join(', ')]);
    }
    if (log_bytes > 0) rows.push(['Лог службы', kb(log_bytes) + over(log_from, d.created_at)]);
    const build = [d.app_version, windows_name(d.os)].filter(Boolean).join(' · ');
    if (build) rows.push(['Версия', build]);
    return rows;
  }

  // failing_entry is the failure after `since` that says which connection
  // broke, by the rule the service's report follows: one naming its session
  // over one that does not, a loss over a blip, the newest among equals. A
  // failure naming no server and protocol names no place. Entries are oldest
  // first; is_failure says which entries are failures.
  function failing_entry(entries, since, is_failure) {
    let blamed = null, blamed_rank = -1;
    for (let i = entries.length - 1; i >= 0 && Date.parse(entries[i].at) > since; i--) {
      const a = entries[i].attrs || {};
      if (!a.node_id || !a.protocol || !is_failure(entries[i])) continue;
      const rank = a.session_id ? (a.kind === 'connection_blip' ? 1 : 2) : 0;
      if (rank > blamed_rank) blamed = entries[i], blamed_rank = rank;
    }
    return blamed;
  }

  const api = { message, attached, plural, windows_name, failing_entry };
  if (typeof module !== 'undefined') module.exports = api;
  root.report_ui = api;
})(typeof window !== 'undefined' ? window : globalThis);
