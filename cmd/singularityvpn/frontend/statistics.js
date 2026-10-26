(function (root) {
  'use strict';
  function duration(seconds) {
    if (typeof seconds !== 'number' || !Number.isFinite(seconds) || seconds < 0) return '';
    seconds = Math.floor(seconds);
    const days = Math.floor(seconds / 86400);
    const hours = Math.floor(seconds % 86400 / 3600);
    const minutes = Math.floor(seconds % 3600 / 60);
    const clock = [hours, minutes, seconds % 60].map(n => String(n).padStart(2, '0')).join(':');
    return (days ? days + ' д. ' : '') + clock;
  }
  if (typeof module !== 'undefined') module.exports = { duration };
  if (!root.document) return;
  let since = null;
  const $ = id => document.getElementById(id);
  function tick() {
    const now = Date.now();
    const valid = Number.isFinite(since) && since <= now;
    $('connection-duration').hidden = !valid;
    $('connection-duration').textContent = valid ? duration((now - since) / 1000) : '';
  }
  function update(status) {
    since = status && ['connected', 'degraded'].includes(status.state) && status.since ? Date.parse(status.since) : null;
    if (Number.isFinite(since)) $('connection-duration').dataset.tip = 'Подключено с ' + new Date(since).toLocaleString('ru-RU');
    else delete $('connection-duration').dataset.tip;
    tick();
    const body = $('statistics-body');
    body.replaceChildren();
    const stats = status && status.statistics;
    if (!stats) { body.textContent = status && status.statistics_error ? 'Не удалось прочитать локальную статистику. Сохранённые данные оставлены без изменений.' : 'Статистика пока недоступна.'; return; }
    if (status.statistics_error) { const warning = document.createElement('p'); warning.className = 'notice'; warning.textContent = 'Не удалось сохранить статистику. Новые показатели пока хранятся только в памяти.'; body.append(warning); }
    const values = [
      ['Всего подключено', duration(stats.total_connected_seconds)],
      ['Подключений', stats.connections],
      ['Обрывов', stats.losses],
      ['Ухудшений связи', stats.degradations],
      ['Коротких провалов', stats.blips],
      ['Восстановлений и смен соединения', stats.reconnections],
      ['Переключений сервера или протокола', stats.switches],
      ['Неудачных подключений', stats.failed_connections],
      ['Неудачных восстановлений', stats.failed_reconnections],
      ['Измерений TCP', stats.probe_samples],
      ['Среднее время TCP-подключения', stats.average_rtt_ms == null ? null : Math.round(stats.average_rtt_ms) + ' мс'],
      ['Средняя доля неудачных TCP-подключений', stats.average_loss_percent == null ? null : Math.round(stats.average_loss_percent * 10) / 10 + '%'],
      ['Измерений UDP', stats.udp_samples || null],
      ['Средние потери UDP', stats.average_udp_loss_percent == null ? null : Math.round(stats.average_udp_loss_percent * 100) / 100 + '%'],
      ['Среднее время UDP-ответа', stats.average_udp_rtt_ms == null ? null : Math.round(stats.average_udp_rtt_ms) + ' мс']
    ];
    for (const [name, value] of values) {
      if (value === undefined || value === null || value === '') continue;
      const row = document.createElement('div'); row.className = 'account-detail';
      const label = document.createElement('span'); label.textContent = name;
      const number = document.createElement('span'); number.textContent = String(value);
      row.append(label, number); body.append(row);
    }
  }
  root.statistics_ui = { update, tick };
})(typeof window !== 'undefined' ? window : globalThis);
