const test = require('node:test');
const assert = require('node:assert/strict');
const themes = require('../frontend/themes.js');
function storage() {
  const data = new Map();
  return { getItem: key => data.get(key) ?? null, setItem: (key, value) => data.set(key, value) };
}
test('validation rejects injection, invalid shapes, arrays and unknown fields', () => {
  assert.deepEqual(themes.validate({ accent: '#ABCDEF', background: 'url(secret)', text: '#123', shape: 'sharp', extra: 'x' }), { accent: '#abcdef' });
  for (const value of [null, [], true, 'bad', 12]) assert.deepEqual(themes.validate(value), {});
});
test('selection stays inside its mode and accepts existing saved keys', () => {
  const disk = storage(); disk.setItem('theme.normal', 'amber'); disk.setItem('theme.gamer', 'graphite');
  const state = themes.create_store(() => disk);
  assert.equal(state.selected('normal'), 'amber'); assert.equal(state.selected('gamer'), 'berserk');
  state.select('normal', 'toxin'); assert.equal(state.selected('normal'), 'amber');
  state.select('gamer', 'synthwave'); assert.equal(state.selected('normal'), 'amber');
});
test('overrides persist independently for each theme and mode', () => {
  const disk = storage(), state = themes.create_store(() => disk);
  state.update('normal', 'amber', { accent: '#112233', shape: 'rounded-square' });
  state.update('gamer', 'toxin', { text: '#abcdef' });
  const restored = themes.create_store(() => disk);
  assert.deepEqual(restored.overrides('normal', 'amber'), { accent: '#112233', shape: 'rounded-square' });
  assert.deepEqual(restored.overrides('normal', 'graphite'), {});
  assert.deepEqual(restored.overrides('gamer', 'toxin'), { text: '#abcdef' });
  state.reset('normal', 'amber');
  assert.deepEqual(state.overrides('normal', 'amber'), {});
  assert.deepEqual(state.overrides('gamer', 'toxin'), { text: '#abcdef' });
});
test('corrupt and hostile stored payloads are ignored', () => {
  const disk = storage(), key = 'theme.editor.v1.normal.amber';
  const state = themes.create_store(() => disk);
  disk.setItem(key, '{broken'); assert.deepEqual(state.overrides('normal', 'amber'), {});
  disk.setItem(key, '{"accent":"red; color:black","shape":"round","text":"#010203"}');
  assert.deepEqual(state.overrides('normal', 'amber'), { shape: 'round', text: '#010203' });
  state.update('normal', 'toxin', { text: '#ffffff' }); assert.deepEqual(state.overrides('normal', 'toxin'), {});
});
test('blocked storage and quota failure retain edits and reset for the session', () => {
  for (const getter of [() => { throw Error('blocked'); }, () => ({ getItem() { throw Error('blocked'); }, setItem() { throw Error('quota'); } })]) {
    const state = themes.create_store(getter);
    state.select('normal', 'graphite'); assert.equal(state.selected('normal'), 'graphite');
    state.update('normal', 'graphite', { background: '#ffffff' });
    assert.deepEqual(state.overrides('normal', 'graphite'), { background: '#ffffff' });
    state.reset('normal', 'graphite'); assert.deepEqual(state.overrides('normal', 'graphite'), {});
  }
});
test('preview applies colors and clears inline overrides on theme switch and reset', () => {
  const properties = new Map();
  global.document = { documentElement: { dataset: {}, style: { setProperty: (k, v) => properties.set(k, v), removeProperty: k => properties.delete(k) } } };
  themes.store.update('normal', 'amber', { accent: '#112233', background: '#101010', text: '#ffffff' });
  themes.apply('normal', 'amber');
  assert.equal(properties.get('--accent-rgb'), '17,34,51');
  assert.equal(properties.get('--ground'), '#101010');
  assert.equal(properties.get('--text'), '#ffffff');
  assert.equal(document.documentElement.dataset.shape, 'round');
  themes.apply('gamer', 'toxin');
  assert.equal(properties.size, 0);
  assert.equal(document.documentElement.dataset.shape, 'rounded-square');
  themes.store.reset('normal', 'amber'); themes.apply('normal', 'amber');
  assert.equal(properties.size, 0);
});

test('Save As persists the complete edited snapshot, independent of later source edits', () => {
  const disk = storage(), state = themes.create_store(() => disk);
  state.update('gamer', 'toxin', { accent: '#123456', shape: 'square', pattern: 'grid' });
  const snapshot = state.settings('gamer', 'toxin');
  const saved = state.save_as('gamer', 'toxin', 'My toxin');
  assert.ok(saved.id);
  state.update('gamer', 'toxin', { accent: '#ffffff', pattern: 'none' });
  const restored = themes.create_store(() => disk);
  assert.equal(restored.selected('gamer'), saved.id);
  assert.equal(restored.selected('normal'), 'singularity');
  assert.deepEqual(restored.settings('gamer', saved.id), snapshot);
  assert.equal(restored.base('gamer', saved.id), 'toxin');
  assert.equal(restored.custom_themes('normal').length, 0);
  restored.update('gamer', saved.id, { shape: 'round', pattern: 'dots' });
  restored.reset('gamer', saved.id);
  assert.deepEqual(restored.settings('gamer', saved.id), snapshot);
  const copy = restored.save_as('gamer', saved.id, 'Copy');
  assert.deepEqual(restored.settings('gamer', copy.id), snapshot);
});

test('names are validated and collisions never overwrite a saved theme', () => {
  const state = themes.create_store(() => storage());
  for (const name of ['', '   ', '<img src=x onerror=alert(1)>', 'a\n', 'x'.repeat(49), {}, null, 'AMBER']) {
    assert.ok(state.save_as('normal', 'amber', name).error);
  }
  const saved = state.save_as('normal', 'amber', '  My theme  ');
  assert.ok(saved.id);
  assert.ok(state.save_as('normal', 'graphite', 'my THEME').error);
  assert.equal(state.custom_themes('normal').length, 1);
  assert.equal(state.custom_themes('normal')[0].name, 'My theme');
  assert.ok(state.save_as('gamer', 'toxin', 'My theme').id);
  assert.equal(state.selected('normal'), saved.id);
});

test('hostile custom records and snapshot fields cannot become executable styles', () => {
  const disk = storage();
  disk.setItem('theme.custom.v1.gamer', JSON.stringify([
    { id: 'custom-1', name: '<svg onload=alert(1)>', base: 'toxin' },
    { id: 'custom-2', name: 'Wrong mode', base: 'amber' },
    { id: 'custom-3', name: 'Safe', base: 'toxin', snapshot: { accent: 'url(x)', pattern: 'url(x)', shape: 'square', text: '#abcdef' } },
    { id: 'custom-3', name: 'Duplicate', base: 'toxin' },
  ]));
  const state = themes.create_store(() => disk);
  assert.equal(state.custom_themes('gamer').length, 1);
  assert.deepEqual(state.custom_themes('gamer')[0].snapshot, { shape: 'square', text: '#abcdef' });
  assert.equal(state.settings('gamer', 'custom-3').pattern, 'molecules');
  disk.setItem('theme.custom.v1.normal', '{');
  assert.deepEqual(state.custom_themes('normal'), []);
});

test('all shapes and patterns survive storage and have distinct preset defaults', () => {
  const disk = storage();
  for (const shape of ['square', 'round', 'rounded-square']) {
    for (const pattern of ['none', 'molecules', 'slashes', 'grid', 'orbits', 'dots']) {
      const state = themes.create_store(() => disk);
      state.update('gamer', 'toxin', { shape, pattern });
      assert.deepEqual(themes.create_store(() => disk).overrides('gamer', 'toxin'), { shape, pattern });
    }
  }
  const state = themes.create_store(() => storage());
  assert.equal(state.settings('gamer', 'toxin').pattern, 'molecules');
  assert.equal(state.settings('gamer', 'berserk').pattern, 'slashes');
  assert.equal(state.settings('gamer', 'synthwave').pattern, 'grid');
  assert.deepEqual(themes.validate({ pattern: 'url(javascript:alert(1))' }), {});
});

test('Save As retains a snapshot during blocked storage and quota failure', () => {
  const state = themes.create_store(() => { throw Error('blocked'); });
  state.update('normal', 'amber', { shape: 'square', pattern: 'molecules' });
  const snapshot = state.settings('normal', 'amber');
  const saved = state.save_as('normal', 'amber', 'Offline');
  assert.equal(state.selected('normal'), saved.id);
  assert.deepEqual(state.settings('normal', saved.id), snapshot);
});
