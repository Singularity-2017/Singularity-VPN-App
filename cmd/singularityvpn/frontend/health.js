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
  if (typeof module !== 'undefined') module.exports = {degraded};
  else root.health_text = {degraded};
})(typeof window !== 'undefined' ? window : globalThis);
