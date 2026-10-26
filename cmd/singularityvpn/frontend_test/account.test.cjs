const { test } = require('node:test');
const assert = require('node:assert/strict');
const ui = require('../frontend/account.js');

test('unknown numeric values never become zero', () => {
  for (const value of [null, undefined, '', NaN]) assert.equal(ui.number(value), '');
  assert.equal(ui.number(0), '0');
  assert.equal(ui.number(12), '12');
});

test('money scales minor units by the currency exponent and refuses unknowns', () => {
  const plain = (text) => text.replace(/\s/g, ' ');
  assert.equal(plain(ui.money(150000, 'RUB')), '1 500 ₽');
  assert.equal(plain(ui.money(150050, 'RUB')), '1 500,50 ₽');
  assert.match(plain(ui.money(1500, 'JPY')), /^1 500 \D+$/);
  assert.equal(plain(ui.money(9007199254740991, 'RUB')), '90 071 992 547 409,91 ₽');
  assert.equal(plain(ui.money(-150005, 'RUB')), '-1 500,05 ₽');
  for (const [amount, currency] of [[null, 'RUB'], [1.5, 'RUB'], [NaN, 'RUB'], [100, 'rub'], [100, ''], [100, undefined], [2 ** 53, 'RUB']]) {
    assert.equal(ui.money(amount, currency), '');
  }
});

test('future expiry uses a relative duration and rejects invalid dates', () => {
  const now = Date.parse('2026-10-25T12:00:00Z');
  assert.equal(ui.expiry('2026-10-27T12:00:00Z', now), 'ещё 2 дн.');
  assert.equal(ui.expiry('2026-10-24T12:00:00Z', now), 'истёк');
  assert.equal(ui.expiry('invalid', now), '');
  assert.equal(ui.expiry(null, now), '');
});
