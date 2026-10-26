(function (root) {
  'use strict';
  const number = (value) => typeof value === 'number' && Number.isFinite(value) ? new Intl.NumberFormat('ru-RU').format(value) : '';
  function expiry(value, now = Date.now()) {
    if (!value) return '';
    const end = Date.parse(value);
    if (!Number.isFinite(end)) return '';
    const left = end - now;
    if (left <= 0) return 'истёк';
    if (left >= 86400000) return 'ещё ' + Math.ceil(left / 86400000) + ' дн.';
    if (left >= 3600000) return 'ещё ' + Math.ceil(left / 3600000) + ' ч';
    return 'ещё ' + Math.max(1, Math.ceil(left / 60000)) + ' мин';
  }
  function ago(value, now = Date.now()) {
    if (!value || !Number.isFinite(Date.parse(value))) return '';
    const age = Math.max(0, Math.floor((now - Date.parse(value)) / 1000));
    if (age < 60) return age + ' с назад';
    if (age < 3600) return Math.floor(age / 60) + ' мин назад';
    if (age < 86400) return Math.floor(age / 3600) + ' ч назад';
    return Math.floor(age / 86400) + ' дн. назад';
  }
  // Minor units scaled by the currency's own exponent, never a fixed 100.
  function money(amount_minor, currency) {
    if (!Number.isSafeInteger(amount_minor) || !/^[A-Z]{3}$/.test(currency || '')) return '';
    try {
      const digits = new Intl.NumberFormat('ru-RU', { style: 'currency', currency }).resolvedOptions().maximumFractionDigits;
      const scale = 10 ** digits, fraction = Math.abs(amount_minor % scale);
      // A decimal string, not amount / scale: a float misrounds near 2^53.
      const whole = String((amount_minor - amount_minor % scale) / scale);
      const decimal = (amount_minor < 0 && whole[0] !== '-' ? '-' : '') + whole + (digits ? '.' + String(fraction).padStart(digits, '0') : '');
      return new Intl.NumberFormat('ru-RU', { style: 'currency', currency, minimumFractionDigits: fraction ? digits : 0, maximumFractionDigits: digits }).format(decimal);
    } catch (_) { return ''; }
  }
  if (typeof module !== 'undefined') module.exports = { number, expiry, money };
  if (!root.document) return;
  const $ = (id) => document.getElementById(id);
  let account = null, generation = 0, loading = false, read_at = 0, active = false, identity = '';
  let identity_status = {};
  function render_identity(status = identity_status) {
    identity_status = status;
    const name = account && account.username || status.username || identity || '';
    $('account-name').textContent = name || 'Аккаунт';
    $('avatar-letter').textContent = name ? name.slice(0, 1).toUpperCase() : '';
    $('avatar-anon').style.display = name ? 'none' : '';
    $('menu-button').dataset.tip = name ? 'Аккаунт ' + name : 'Аккаунт';
  }
  let panel_history = [], current_panel = null;
  function make(tag, text, class_name) {
    const el = document.createElement(tag);
    if (text !== undefined) el.textContent = text;
    if (class_name) el.className = class_name;
    return el;
  }
  function row(parent, label, value) {
    if (value === null || value === undefined || value === '') return;
    const el = make('div', undefined, 'account-detail');
    el.append(make('span', label, 'muted'), make('span', String(value)));
    parent.append(el);
    return el;
  }
  function dated(parent, label, value) {
    if (!value || !Number.isFinite(Date.parse(value))) return;
    const el = make('div', undefined, 'account-detail');
    const time = make('span', expiry(value));
    time.dataset.expiry = value;
    time.dataset.tip = new Date(value).toLocaleString('ru-RU');
    time.title = time.dataset.tip;
    time.tabIndex = 0;
    el.append(make('span', label, 'muted'), time);
    parent.append(el);
  }
  function moment(parent, label, value) {
    if (!value || !Number.isFinite(Date.parse(value))) return;
    const el = make('div', undefined, 'account-detail');
    const time = make('span', ago(value));
    time.dataset.accountTime = value;
    time.dataset.tip = new Date(value).toLocaleString('ru-RU');
    time.title = time.dataset.tip;
    time.tabIndex = 0;
    el.append(make('span', label, 'muted'), time);
    parent.append(el);
  }
  function show_panel(title, draw, nested = false) {
    if (nested && current_panel) panel_history.push(current_panel);
    else panel_history = [];
    current_panel = { title, draw };
    $('menu').hidden = false;
    $('menu-button').setAttribute('aria-expanded', 'true');
    for (const el of $('menu').querySelectorAll('.pop-level')) el.hidden = true;
    $('account-panel').hidden = false;
    $('account-panel-title').textContent = title;
    const body = $('account-panel-body');
    body.replaceChildren();
    draw(body);
    body.scrollTop = 0;
    $('account-back').focus();
  }
  function plan_name() {
    if (!account) return '';
    return account.plan && (account.plan.name || account.plan.id) || '';
  }
  function open_plan() {
    show_panel('Тариф', (body) => {
      if (!account) { body.append(make('div', 'Не удалось загрузить тариф.', 'pop-note')); return; }
      row(body, 'Тариф', plan_name());
      if (account.plan.subscription_active === true) dated(body, 'Срок подписки', account.plan.expires_at);
      row(body, 'Профилей', number(account.plan.client_limit));
      if (typeof account.plan.target_kbps === 'number') {
        const speed = row(body, 'Целевая скорость, Мбит/с', number(account.plan.target_kbps / 1000));
        speed.dataset.tip = 'Целевая скорость доступна не на всех серверах. Это условие тарифа, а не замер текущего соединения.';
        speed.tabIndex = 0;
      }
      if (typeof account.plan.rate_kbps === 'number') row(body, 'Гарантировано тарифом, Мбит/с', number(account.plan.rate_kbps / 1000));
      if (account.plan.personal_bandwidth === true) {
        row(body, 'Персональная скорость, Мбит/с', typeof account.plan.effective_rate_kbps === 'number' ? number(account.plan.effective_rate_kbps / 1000) : '');
        row(body, 'Персональный максимум, Мбит/с', typeof account.plan.effective_ceil_kbps === 'number' ? number(account.plan.effective_ceil_kbps / 1000) : '');
      }
      row(body, 'Цена', money(account.plan.amount_minor, account.plan.currency));
      if (typeof account.plan.period_days === 'number') row(body, 'Период, дней', number(account.plan.period_days));
      if (account.section_errors && account.section_errors.policies) row(body, 'Условия тарифа', 'Не удалось обновить');
      if (account.section_errors && account.section_errors.prices) row(body, 'Цена', 'Не удалось обновить');
      if (account.plan.sponsored_place === true) row(body, 'Доступ', 'Бесплатное место');
      else if (account.plan.subscription_active === false) row(body, 'Подписка', 'Без платной подписки');
      if (Array.isArray(account.payments) || account.section_errors && account.section_errors.payments) {
        // Allocations belong to the tariff: who paid is not said, so they are never spending.
        const credited = make('button', undefined, 'opt line opt-row');
        credited.type = 'button';
        credited.append(make('span', 'Зачисления', 'opt-name'), make('span', '›'));
        credited.dataset.tip = 'Оплаченные периоды этого тарифа, в том числе оплаченные другими.';
        credited.addEventListener('click', () => show_panel('Зачисления на тариф', draw_allocations, true));
        body.append(credited);
      }
      const portal = make('button', 'Открыть личный кабинет', 'plain');
      portal.dataset.tip = 'Продление и смена тарифа — через администратора в личном кабинете.';
      portal.addEventListener('click', async () => {
        const app = root.go && root.go.main && root.go.main.App;
        if (!app || typeof app.Open_account_portal !== 'function') return;
        const result = await app.Open_account_portal().catch(() => ({code:'failed'}));
        if (result && result.code) portal.textContent = 'Не удалось открыть личный кабинет';
      });
      body.append(portal);
    });
  }
  function draw_allocations(body) {
    if (!Array.isArray(account.payments) || account.section_errors && account.section_errors.payments) {
      body.append(make('div', 'Зачисления временно недоступны.', 'pop-note'));
      return;
    }
    for (const p of account.payments) {
      const card = make('div', undefined, 'account-card');
      row(card, 'Зачислено на тариф', money(p.amount_minor, p.payment_currency));
      const payment = row(card, 'Исходный платёж', money(p.payment_amount_minor, p.payment_currency));
      if (payment) { payment.dataset.tip = 'Один исходный платёж может относиться к нескольким зачислениям. Плательщиком могли быть не вы.'; payment.tabIndex = 0; }
      moment(card, 'Оплата', p.payment_occurred_at);
      dated(card, 'Оплачено до', p.period_end);
      body.append(card);
    }
    if (!account.payments.length) body.append(make('div', 'Зачислений пока нет.', 'pop-note'));
  }
  // The control plane's own wording for its total (docs/ACCOUNT_SPENDING there).
  const spending_tip = 'Записанные успешные платежи этого аккаунта, включая оплату за других. До вычета возвратов; валюты не пересчитываются. Учитываются только платежи, занесённые в сервис.';
  let spending_fresh_from = Infinity;
  function spending_failed() {
    return !account || !account.spending || !!(account.section_errors && account.section_errors.spending);
  }
  // The total exactly as the control plane computed it; pages are never summed.
  function spent_text() {
    if (spending_failed() || account.spending.status !== 'available' || !Array.isArray(account.spending.totals)) return '';
    if (!account.spending.totals.length) return 'Платежей нет';
    const parts = account.spending.totals.map((total) => money(total.amount_minor, total.currency));
    return parts.every(Boolean) ? parts.join(' · ') : '';
  }
  function draw_spending(body) {
    const spending = account && account.spending;
    if (spending_failed() || !Array.isArray(spending.transactions)) {
      body.append(make('div', 'Платежи временно недоступны.', 'pop-note'));
      return;
    }
    if (spending.restarted) {
      body.append(make('div', 'Список устарел и загружен заново.', 'pop-note'));
      spending.restarted = false;
    }
    if (spending.coverage === 'verified_paid_payments_only') body.append(make('div', 'Итога нет, поэтому список может быть неполным: в нём только проверенные платежи.', 'pop-note'));
    spending.transactions.forEach((t, i) => {
      const line = make('div', undefined, i >= spending_fresh_from ? 'account-detail fresh' : 'account-detail');
      const time = make('span', ago(t.occurred_at), 'muted');
      time.dataset.accountTime = t.occurred_at;
      time.dataset.tip = new Date(t.occurred_at).toLocaleString('ru-RU');
      time.title = time.dataset.tip;
      time.tabIndex = 0;
      line.append(make('span', money(t.amount_minor, t.currency)), time);
      body.append(line);
    });
    if (!spending.transactions.length) body.append(make('div', 'Платежей пока нет.', 'pop-note'));
    if (spending.next_cursor) {
      const more = make('button', 'Показать ещё', 'plain spending-more');
      more.type = 'button';
      more.addEventListener('click', () => more_spending(more, spending));
      body.append(more);
    }
  }
  // Opening the menu always starts at its main level, so a hidden panel is never kept.
  function spending_on_screen() {
    return !!current_panel && current_panel.draw === draw_spending && !$('menu').hidden && !$('account-panel').hidden;
  }
  // before is the list the button was drawn under; load() keeps it while it is on screen.
  async function more_spending(button, before) {
    const app = root.go && root.go.main && root.go.main.App;
    if (!app || typeof app.Spending_page !== 'function' || !account || account.spending !== before) return;
    button.disabled = true;
    const result = await app.Spending_page(before.next_cursor).catch(() => null);
    if (!account || account.spending !== before) { button.disabled = false; return; }
    if (!result || result.error || !result.spending) {
      button.disabled = false;
      button.textContent = 'Не удалось загрузить — повторить';
      return;
    }
    const page = result.spending;
    if (!page.restarted) page.transactions = before.transactions.concat(page.transactions || []);
    account.spending = page;
    if (!spending_on_screen()) return;
    const body = $('account-panel-body'), top = body.scrollTop;
    spending_fresh_from = page.restarted ? Infinity : before.transactions.length;
    body.replaceChildren();
    draw_spending(body);
    spending_fresh_from = Infinity;
    body.scrollTop = page.restarted ? 0 : top;
    const next = body.querySelector('.spending-more') || body.querySelector('.fresh [tabindex]');
    if (next) next.focus();
  }
  function render() {
    const name = plan_name();
    $('account-plan').hidden = !active || !name;
    const target = account && account.plan && account.plan.target_kbps;
    const badge = $('account-plan');
    badge.replaceChildren();
    if (name) badge.append(make('span', name, 'plan-name'));
    if (name && typeof target === 'number' && Number.isFinite(target)) badge.append(make('span', number(target / 1000) + ' Мбит/с', 'plan-speed'));
    $('account-plan').dataset.tip = 'Условия тарифа. Целевая скорость — не замер текущего соединения.';
    if (root.help_ui) root.help_ui.account(account);
    const items = $('account-items');
    const profile_row = $('profile-row');
    if (profile_row && profile_row.parentElement === items) $('menu-main').append(profile_row);
    items.replaceChildren();
    if (!account) return;
    render_identity();
    function item(title, value, draw) {
      const b = make('button', undefined, 'opt line opt-row');
      b.append(make('span', title, 'opt-name'));
      if (value !== '') b.append(make('span', value, 'opt-value'));
      if (draw) { b.append(make('span', '›')); b.addEventListener('click', () => show_panel(title, draw)); }
      else { b.type = 'button'; b.addEventListener('click', () => load(true)); b.dataset.tip = 'Обновить данные аккаунта'; }
      items.append(b);
    }
    const credits = number(account.credits);
    $('header-credits').hidden = credits === '';
    $('header-credits').textContent = credits;
    if (credits !== '') item('Кредиты', credits);
    items.append($('profile-row')); 
    item('Вклад в сеть', '', (body) => {
      const finance = make('button', undefined, 'opt line opt-row');
      const spent = spent_text();
      finance.append(make('span', 'Потрачено', 'opt-name'), make('span', spent || 'Сумма недоступна', 'opt-value'), make('span', '›'));
      finance.dataset.tip = spent ? spending_tip : spending_failed() ? 'Не удалось загрузить платежи. Откройте меню аккаунта ещё раз, чтобы повторить.' : 'Не все записанные платежи прошли проверку, поэтому итога нет. В списке — только проверенные.';
      finance.addEventListener('click', () => show_panel('Платежи', draw_spending, true));
      const feedback = make('button', 'История обратной связи ›', 'opt line opt-row');
      feedback.addEventListener('click', () => show_panel('История обратной связи', draw_reports, true));
      body.append(finance, feedback);
    });
    function draw_reports(body) {
      row(body, 'Вклад', number(account.network_score));
      const summary = account.contribution_summary;
      if (summary) {
        row(body, 'Сообщений', number(summary.reports));
        row(body, 'Багов', number(summary.bugs));
        row(body, 'Исправлено', number(summary.fixed));
      }
      for (const report of account.reports || []) {
        const card = make('div', undefined, 'account-card');
        row(card, report.code || 'Сообщение', ({bug:'Баг',idea:'Идея',duplicate:'Повтор','not-a-bug':'Не баг','needs-info':'Нужны подробности'})[report.verdict] || report.verdict || 'Ждёт разбора');
        if (report.message) card.append(make('div', report.message, 'account-message'));
        const score = number(report.score);
        if (score) {
          const line = row(card, 'Оценка', score + (report.score_source === 'human' ? ' · вручную' : report.score_source === 'ai' ? ' · ИИ' : ''));
          if (report.score_reason) { line.dataset.tip = report.score_reason; line.tabIndex = 0; }
        }
        moment(card, 'Отправлено', report.created_at);
        if (report.fixed_commit) row(card, 'Исправление', 'Исправлено');
        body.append(card);
      }
      if (Array.isArray(account.reports) && !account.reports.length) body.append(make('div', 'Сообщений пока нет.', 'pop-note'));
      if (!Array.isArray(account.reports) || account.section_errors && account.section_errors.contribution) body.append(make('div', 'История вклада временно недоступна.', 'pop-note'));
    }
    if (Array.isArray(account.referrals) || account.referral_code || typeof account.invite_tickets === 'number') item('Приглашения', number(account.invite_tickets), (body) => {
      row(body, 'Доступно', number(account.invite_tickets));
      row(body, 'Код приглашения', account.referral_code);
      row(body, 'Вас пригласил', account.referrer && account.referrer.username);
      row(body, 'Бесплатных мест занято', number(account.sponsored_used));
      row(body, 'Бесплатных мест всего', number(account.sponsored_total));
      for (const ref of account.referrals || []) row(body, ref.referee_username || 'Приглашённый', ref.sponsored === true ? 'Бесплатное место' : 'Приглашён');
      if (Array.isArray(account.referrals) && !account.referrals.length) body.append(make('div', 'Приглашённых пока нет.', 'pop-note'));
      if (account.section_errors && account.section_errors.referrals) body.append(make('div', 'Список приглашённых временно недоступен.', 'pop-note'));
    });
    if (Array.isArray(account.devices)) item('Устройства', number(account.devices.length), (body) => {
      for (const d of account.devices) {
        const card = make('div', undefined, 'account-card');
        const device_name = make('div', undefined, 'account-detail');
        device_name.append(make('span', d.name || 'Устройство'));
        if (d.current) device_name.append(make('span', 'Это устройство', 'muted'));
        card.append(device_name);
        moment(card, 'Последняя активность', d.last_seen_at);
        dated(card, 'Доступ', d.expires_at);
        if (d.revoked_at) row(card, 'Состояние', 'Доступ отозван');
        body.append(card);
      }
      if (!account.devices.length) body.append(make('div', 'Устройств пока нет.', 'pop-note'));
    });
    for (const [section, title] of Object.entries({referrals:'Приглашённые', devices:'Устройства'})) {
      if (section === 'referrals' && (account.referral_code || typeof account.invite_tickets === 'number')) continue;
      if (account.section_errors && account.section_errors[section]) item(title, 'Не удалось загрузить', (body) => {
        body.append(make('div', 'Раздел временно недоступен. Закройте и откройте меню аккаунта, чтобы повторить.', 'pop-note'));
      });
    }
  }
  async function load(force = false) {
    if (!active || loading || (!force && Date.now() - read_at < 60000)) return;
    const app = root.go && root.go.main && root.go.main.App;
    if (!app || typeof app.Account !== 'function') return;
    const request = generation;
    loading = true;
    read_at = Date.now();
    try {
      const result = await app.Account();
      if (request !== generation || !active) return;
      if (!result || result.error) { $('account-error').hidden = false; return; }
      // A background reload must not cut short the pages the person is reading.
      if (result.account && account && spending_on_screen()) result.account.spending = account.spending;
      account = result.account;
      read_at = Date.now();
      $('account-error').hidden = true;
      render();
    } catch (_) {
      if (request === generation && active) $('account-error').hidden = false;
    } finally { if (request === generation) loading = false; }
  }
  function update(signed_in, username = '') {
    if (active && username && identity && username !== identity) update(false);
    if (username) identity = username;
    if (!signed_in) {
      if (active) generation++;
      active = false; account = null; loading = false; read_at = 0;
      identity = ''; identity_status = {};
      $('header-credits').hidden = true;
      $('header-credits').textContent = '';
      $('account-plan').hidden = true;
      $('account-plan').textContent = '';
      $('account-name').textContent = '';
      panel_history = []; current_panel = null;
      if (root.help_ui) root.help_ui.reset();
      const profile_row = $('profile-row');
      if (profile_row && profile_row.parentElement === $('account-items')) $('menu-main').append(profile_row);
      $('account-items').replaceChildren();
      $('account-panel-body').replaceChildren();
      $('account-error').hidden = true;
      return;
    }
    active = true;
    load();
  }
  function start() {
    $('header-credits').addEventListener('click', () => $('menu-button').click());
    $('account-plan').addEventListener('click', (event) => {
      event.stopPropagation();
      root.dispatchEvent(new CustomEvent('account-plan-open'));
      open_plan();
    });
    $('account-back').addEventListener('click', () => {
      if (panel_history.length) {
        const previous = panel_history.pop();
        const history = panel_history.slice();
        show_panel(previous.title, previous.draw);
        panel_history = history;
        return;
      }
      $('account-panel').hidden = true;
      $('menu-main').hidden = false;
      current_panel = null;
      const first = $('menu-main').querySelector('button');
      if (first) first.focus();
    });
    $('menu-button').addEventListener('click', () => load(true));
    setInterval(() => {
      for (const el of document.querySelectorAll('[data-expiry]')) el.textContent = expiry(el.dataset.expiry);
      for (const el of document.querySelectorAll('[data-account-time]')) el.textContent = ago(el.dataset.accountTime);
    }, 1000);
  }
  root.account_ui = { update, render_identity, refresh: () => load(true) };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start);
  else start();
})(typeof window !== 'undefined' ? window : globalThis);
