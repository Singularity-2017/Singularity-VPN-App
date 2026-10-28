const {test} = require('node:test');
const assert = require('node:assert/strict');
const health = require('../frontend/health.js');
test('actual degradation causes are stated without an either-or guess', () => {
  assert.equal(health.degraded({degraded_probes:true, loss_percent:25}), 'Не прошли 25% контрольных TCP-соединений');
  assert.equal(health.degraded({degraded_latency:true, rtt_ms:100, baseline_rtt_ms:20}), 'TCP-подключение при проверке — 100 мс вместо 20 мс');
  const both = health.degraded({degraded_latency:'true',degraded_probes:'true',rtt_ms:'100',baseline_rtt_ms:'20',loss_percent:'25'});
  assert.match(both,/100 мс вместо 20 мс/);
  assert.match(both,/25%/);
  assert.doesNotMatch(both,/или/);
  assert.doesNotMatch(health.degraded({degraded_latency:true}),/0|undefined|NaN/);
});
test('probes that got through only on a resent SYN read as lost packets, not latency', () => {
  const text = health.degraded({degraded_resent:'true', resent_percent:'100', loss_percent:'0'});
  assert.match(text, /100%/);
  assert.doesNotMatch(text, /мс/);
  assert.doesNotMatch(health.degraded({degraded_resent:true}), /undefined|NaN|\d/);
});
test('lost UDP datagrams are stated as such', () => {
  const text = health.degraded({degraded_udp:'true', udp_loss_percent:'12.5'});
  assert.match(text, /UDP/);
  assert.match(text, /13%/);
  assert.doesNotMatch(health.degraded({degraded_udp:true}), /undefined|NaN|\d/);
});
test('what OpenVPN reports reads as what happened, not as its log label', () => {
  const [restart, tone] = health.openvpn('ping-restart');
  assert.match(restart, /^OpenVPN: /);
  assert.doesNotMatch(restart, /ping-restart/);
  assert.equal(tone, 'warn');
  // Stopping it is the service's own doing when it reconnects or switches.
  assert.equal(health.openvpn('SIGTERM')[1], '');
  for (const marker of ['Inactivity timeout', 'Connection reset', 'Connection timed out', 'SIGUSR1', 'TLS handshake failed', 'AUTH_FAILED', 'Exiting due to fatal error', 'Options error:', 'Cannot open TUN/TAP dev', 'Cannot load private key', 'ERROR: Windows route add command failed']) {
    assert.ok(!health.openvpn(marker)[0].includes(marker), marker);
  }
  // A label added to the service later is shown as it is rather than hidden.
  assert.equal(health.openvpn('Something new')[0], 'OpenVPN: Something new');
});
