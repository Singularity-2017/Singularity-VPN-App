(function(root) {
  'use strict';
  const yes = (v) => v === true || v === 'true';
  const numeric = (v) => (typeof v === 'number' || typeof v === 'string' && v.trim() !== '') && Number.isFinite(Number(v));
  function degraded(value) {
    const parts = [];
    if (yes(value.degraded_latency)) {
      let text = 'Выросла задержка контрольного соединения';
      if (numeric(value.rtt_ms) && numeric(value.baseline_rtt_ms)) text = 'TCP-подключение при проверке — ' + Math.round(Number(value.rtt_ms)) + ' мс вместо ' + Math.round(Number(value.baseline_rtt_ms)) + ' мс';
      parts.push(text);
    }
    if (yes(value.degraded_resent)) parts.push('Терялись пакеты: ' + (numeric(value.resent_percent) ? Math.round(Number(value.resent_percent)) + '% контрольных TCP-подключений прошли' : 'часть контрольных TCP-подключений прошла') + ' только со второй попытки');
    if (yes(value.degraded_udp)) parts.push('Терялись UDP-пакеты: ' + (numeric(value.udp_loss_percent) ? Math.round(Number(value.udp_loss_percent)) + '% контрольных DNS-запросов остались' : 'часть контрольных DNS-запросов осталась') + ' без ответа');
    if (yes(value.degraded_probes)) parts.push(numeric(value.loss_percent) ? 'Не прошли ' + Math.round(Number(value.loss_percent)) + '% контрольных TCP-соединений' : 'Часть контрольных TCP-соединений не прошла');
    return parts.join('; ') || 'Ухудшение связи — причина не передана службой';
  }
  // openvpn_text is what OpenVPN's own log labels mean, in the words of the
  // journal. The service passes on fixed labels only, never the log line.
  const openvpn_text = {
    'ping-restart': 'сервер перестал отвечать — соединение перезапущено',
    'Inactivity timeout': 'сервер долго молчал — соединение перезапущено',
    'Connection reset': 'соединение с сервером оборвалось',
    'Connection timed out': 'сервер не ответил вовремя',
    'SIGUSR1': 'соединение перезапускается',
    'SIGTERM': 'остановлен',
    'TLS handshake failed': 'рукопожатие с сервером не прошло',
    'AUTH_FAILED': 'сервер не принял ключ подключения',
    'Exiting due to fatal error': 'завершился с ошибкой',
    'Options error:': 'не принял настройки подключения',
    'All wintun adapters on this system are currently in use': 'все адаптеры Wintun заняты',
    'Cannot open TUN/TAP dev': 'не смог открыть сетевой адаптер',
    'Cannot load inline certificate': 'не смог прочитать сертификат подключения',
    'Cannot load CA certificate': 'не смог прочитать сертификат сервера',
    'Cannot load private key': 'не смог прочитать ключ подключения',
    'ERROR: Windows route add command failed': 'не смог добавить маршрут'
  };
  // openvpn is [text, tone] for one "openvpn event". Stopping it is the
  // service's own doing when it reconnects or switches, not a problem.
  function openvpn(event) {
    return ['OpenVPN: ' + (openvpn_text[event] || event), event === 'SIGTERM' ? '' : 'warn'];
  }
  if (typeof module !== 'undefined') module.exports = {degraded, openvpn};
  else root.health_text = {degraded, openvpn};
})(typeof window !== 'undefined' ? window : globalThis);
