(function (root) {
  'use strict';
  const $ = (id) => document.getElementById(id);
  let account_data = null, draft = '', kind = 'problem', generation = 0, sending = false, last_status = '';
  function make(tag, text, class_name) {
    const element = document.createElement(tag);
    if (text !== undefined) element.textContent = text;
    if (class_name) element.className = class_name;
    return element;
  }
  function button(text, action) {
    const element = make('button', text, 'opt line');
    element.type = 'button';
    element.addEventListener('click', action);
    return element;
  }
  function panel(title) {
    const body = $('help');
    body.replaceChildren(button('‹ ' + title, open));
    body.firstElementChild.setAttribute('aria-label', 'Назад к помощи');
    body.firstElementChild.focus();
    body.scrollTop = 0;
    return body;
  }
  function faq() {
    const body = panel('FAQ');
    const plan = account_data && account_data.plan;
    const target = plan && plan.target_kbps;
    const tariff = plan ? 'Ваш тариф: ' + (plan.name || plan.id) + '. ' : '';
    const speed = typeof target === 'number' && Number.isFinite(target)
      ? 'Целевая скорость тарифа — ' + new Intl.NumberFormat('ru-RU').format(target / 1000) + ' Мбит/с. Это условие тарифа, а не замер текущего соединения; она доступна не на всех серверах.'
      : 'Целевая скорость сейчас недоступна. Условия тарифа можно проверить в меню аккаунта.';
    for (const [question, answer] of [
      ['Как подключиться и войти в аккаунт?', 'Введите имя и пароль аккаунта Singularity, затем нажмите кнопку подключения. Сейчас вход выполняется по паролю. Вход по коду или через сторонние аккаунты в приложении не предусмотрен.'],
      ['Нужно ли скачивать профиль и устанавливать отдельный клиент?', 'Для этого приложения — нет: компоненты туннеля входят в установщик. Скачивание и импорт профиля из личного кабинета нужны при использовании отдельного клиента выбранного протокола на другом устройстве.'],
      ['Какой протокол выбрать?', 'Обычно — «Авто»: протокол подбирается под вашу сеть и меняется сам, если перестал проходить. Выбрать вручную можно под переключателем режима. AmneziaWG труднее распознать и заблокировать, WireGuard быстрее, OpenVPN медленнее, но иногда проходит там, где другие не проходят; он ведёт весь трафик компьютера, поэтому игры с ним ускоряются через полный VPN. Выбранный протокол держится и при восстановлении связи.'],
      ['Как сменить или восстановить пароль?', 'Откройте личный кабинет и нажмите на своё имя вверху страницы. Для смены нужен текущий пароль. Если пароль забыт, попросите администратора сбросить его, затем смените выданный пароль на свой.'],
      ['Чем отличаются Антизапрет и VPN?', 'В режиме «Антизапрет» через сервер идут заблокированные в России адреса, остальное — напрямую. В режиме «VPN» через сервер идёт весь трафик компьютера. Выберите режим рядом с кнопкой подключения. Это выбор маршрутизации, а не протокола туннеля.'],
      ['Что меняет игровой режим?', 'Игровой режим показывает игры, их буст и отдельную тему оформления. Нажатие на карточку включает буст и запускает игру, если её запуск доступен; повторное нажатие выключает буст. Антизапрет или VPN продолжают работать. Переключение между журналом и играми само по себе не выключает буст.'],
      ['Всегда ли буст затрагивает только игру?', 'Не всегда: для некоторых игр с чувствительным античитом или без списка исполняемых файлов через туннель направляется весь компьютер. Проверяйте пояснение на карточке игры.'],
      ['Почему игровой ping приблизительный или отсутствует?', 'Во время матча приложение пытается измерить задержку до обнаруженного сервера матча через VPN. Если сервер не отвечает на ping, используется сумма двух замеров: компьютер → VPN-сервер и VPN-сервер → регион игры. Это приблизительная оценка, она может отличаться от показателя в игре. Наведите указатель на число, чтобы увидеть источник. Пока замера нет, число не показывается.'],
      ['Зачем подтверждать сервер матча?', 'Если приложение предлагает проверить обнаруженный сервер, сравните его с тем, что показывает игра, и ответьте на карточке. Само обнаружение ещё не означает, что сервер определён верно.'],
      ['Какая скорость у моего тарифа?', tariff + speed],
      ['Подписка и VPN-профиль заканчиваются одновременно?', 'Нет. Срок подписки и срок VPN-профиля независимы. Смотрите условия и срок активной подписки в тарифе, а данные профиля — в списке профилей аккаунта. Старый срок неактивной подписки сам по себе не определяет доступ бесплатного или спонсорского аккаунта.'],
      ['Как продлить или сменить тариф?', 'Откройте тариф под значком аккаунта, затем личный кабинет. Продление и смена тарифа доступны через администратора в личном кабинете.'],
      ['Можно ли использовать один профиль на двух устройствах?', 'Один профиль нельзя использовать на двух устройствах одновременно: когда подключится второе, первое отключится. Выбор профиля доступен в меню аккаунта.'],
      ['Сколько устройств можно подключить?', 'Лимит профилей зависит от тарифа; смотрите доступные условия под значком аккаунта. Для другого устройства используйте отдельный профиль. Смена сервера для уже существующего устройства в личном кабинете сама по себе не требует создавать ещё одно устройство.'],
      ['Обязательна ли платная подписка?', 'У сервиса есть бесплатный тариф и спонсорские места. Платная подписка меняет условия тарифа; возможность регистрации и состояние конкретного профиля проверяются отдельно. Спонсорское место предоставляет пригласивший пользователь, и он может его отозвать. Актуальные условия вашего доступа смотрите в аккаунте.'],
      ['Что означают кредиты и приглашения?', 'Кредиты и приглашения доступны в меню аккаунта, когда сервис возвращает эти данные. В приглашениях можно посмотреть свой код, приглашённых и спонсорские места. Для действий с аккаунтом откройте личный кабинет; кредиты не являются замером скорости или числом свободных профилей.'],
      ['Что защищает VPN и что видит сервер?', 'Туннель защищает участок связи между устройством и VPN-сервером. Он не делает пользователя полностью анонимным: серверу могут быть видны адреса и домены назначений, а сайты по-прежнему могут узнавать вас по входу в аккаунт. В Антизапрете трафик вне выбранных маршрутов идёт напрямую.'],
      ['Что делать, если соединение не устанавливается или ухудшилось?', 'Откройте «Журнал» и прочитайте причину последнего события. Проверьте, работает ли интернет без VPN, какой выбран сервер и профиль, затем попробуйте подключиться повторно или выбрать другой доступный сервер. Предупреждение о задержке или неудачных контрольных TCP-соединениях относится к проверкам соединения, а не обязательно к потерям пакетов в игре.'],
      ['Как посмотреть журнал в компактном окне?', 'Нажмите «Журнал» рядом с состоянием соединения. Кнопка «Игры» возвращает карточки без смены игрового режима. Если журнал не загрузился, воспользуйтесь повторной загрузкой; длинный журнал прокручивается.'],
      ['Как сообщить о проблеме или предложить улучшение?', 'Если связь сбоит без видимой причины, рядом с проблемой появляется кнопка «Сообщить о сбое» — текст уже написан, допишите, что вы делали в это время. В остальных случаях: Помощь → Сообщить о баге, «Баг» или «Идея». К сообщению о баге прикладываются журнал, сведения о сети, программы на экране и лог службы; список виден до отправки, галочка его отключает. Не отправляйте пароли или ключи. При ошибке отправки текст остаётся в форме, успешная отправка подтверждается кодом сообщения.'],
      ['Как сохранить свою тему?', 'Настройки → Тема → Изменить. Цвета, форма и узор применяются сразу. Нажмите «Сохранить как», введите уникальное имя и нажмите «Сохранить» или Enter. «Отмена» или Escape закрывает ввод без создания темы. Обычный и игровой режимы имеют отдельный выбор темы.'],
      ['Где посмотреть отправленные сообщения?', 'Аккаунт → Вклад в сеть → История обратной связи. Там отображаются сообщения, результаты разбора и оценки, которые вернул сервис.'],
    ]) {
      const detail = make('details');
      detail.append(make('summary', question), make('p', answer));
      body.append(detail);
    }
  }
  // The diagnostics a problem report carries: built by the service when
  // the form opens, shown before sending, and sent by its id, so what goes
  // is what was shown. A draft older than ten minutes no longer describes
  // the moment and is built again.
  let attach = true, attach_apps = true, problem_draft = null, draft_loading = false, draft_failed = false, prefill = '';
  async function load_draft() {
    if (kind !== 'problem' || !attach || draft_loading) return;
    if (problem_draft && Date.now() - problem_draft.at < 10 * 60000) return;
    const app = root.go && root.go.main && root.go.main.App;
    if (!app || typeof app.Problem_draft !== 'function') { draft_failed = true; render_attach(); return; }
    const request = generation;
    draft_loading = true; draft_failed = false; render_attach();
    let answer = null;
    try { answer = await app.Problem_draft(attach_apps); } catch (_) { answer = null; }
    if (request !== generation) return;
    draft_loading = false;
    if (answer && !answer.error && answer.draft && answer.draft.id) {
      problem_draft = { id: answer.draft.id, diagnostics: answer.draft.diagnostics || {}, log: answer.draft.log || '', log_bytes: answer.draft.log_bytes || 0, at: Date.now() };
    } else {
      problem_draft = null; draft_failed = true;
    }
    render_attach();
  }
  function render_attach() {
    const box = $('feedback-attach');
    if (!box) return;
    box.hidden = kind !== 'problem';
    const toggle = make('label', undefined, 'feedback-check');
    const check = make('input');
    check.type = 'checkbox'; check.checked = attach; check.disabled = sending;
    check.addEventListener('change', () => { attach = check.checked; render_attach(); load_draft(); });
    toggle.append(check, document.createTextNode('Приложить сведения для разбора'));
    const parts = [toggle];
    // Which programs were on the screen is the one part that says what the
    // person was doing, so it has a switch of its own.
    if (attach) {
      const apps_toggle = make('label', undefined, 'feedback-check');
      const apps_check = make('input');
      apps_check.type = 'checkbox'; apps_check.checked = attach_apps; apps_check.disabled = sending || draft_loading;
      apps_check.addEventListener('change', () => { attach_apps = apps_check.checked; problem_draft = null; load_draft(); });
      apps_toggle.append(apps_check, document.createTextNode('и программы на экране'));
      parts.push(apps_toggle);
    }
    if (attach && draft_loading) parts.push(make('div', 'Собираем сведения…', 'feedback-note'));
    else if (attach && draft_failed) parts.push(make('div', 'Сведения собрать не удалось — сообщение уйдёт без них.', 'feedback-note'));
    else if (attach && problem_draft) {
      const rows = root.report_ui.attached(problem_draft.diagnostics, problem_draft.log_bytes);
      const what = make('details', undefined, 'feedback-attached');
      what.append(make('summary', 'Что уйдёт · ' + rows.length));
      const list = make('ul');
      for (const [title, detail] of rows) {
        const item = make('li');
        item.append(make('b', title), document.createTextNode(' — ' + detail));
        list.append(item);
      }
      const raw = make('details');
      raw.append(make('summary', 'Сведения как есть'), make('pre', JSON.stringify(problem_draft.diagnostics, null, 2)));
      what.append(list, raw);
      if (problem_draft.log) {
        const log = make('details');
        log.append(make('summary', 'Лог службы как есть'), make('pre', problem_draft.log));
        what.append(log);
      }
      parts.push(what);
    }
    box.replaceChildren(...parts);
  }
  function report() {
    const body = panel('Сообщить о баге');
    const form = make('form', undefined, 'feedback-form');
    const label = make('label', 'Тип сообщения');
    const select = make('select');
    select.id = 'feedback-kind';
    label.htmlFor = select.id;
    for (const [value, title] of [['problem', 'Баг'], ['idea', 'Идея']]) {
      const option = make('option', title); option.value = value; select.append(option);
    }
    select.value = kind;
    select.disabled = sending;
    const message_label = make('label', 'Сообщение');
    const message = make('textarea');
    message.id = 'feedback-message'; message_label.htmlFor = message.id;
    message.required = true; message.maxLength = 2000; message.rows = 6; message.value = draft;
    message.disabled = sending;
    const hint = () => { message.placeholder = kind === 'problem' ? 'Что случилось и что вы делали в это время: игра, звонок, видео.' : ''; };
    hint();
    message.addEventListener('input', () => { draft = message.value; });
    select.addEventListener('change', () => { kind = select.value; hint(); render_attach(); load_draft(); });
    const attach_box = make('div', undefined, 'feedback-attach');
    attach_box.id = 'feedback-attach';
    const status = make('div', last_status, 'feedback-status');
    status.id = 'feedback-status';
    status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
    const submit = make('button', sending ? 'Отправляем…' : 'Отправить', 'plain');
    submit.type = 'submit'; submit.disabled = sending;
    form.append(label, select, message_label, message, attach_box, submit, status);
    body.append(form);
    render_attach();
    load_draft();
    form.addEventListener('submit', async (event) => {
      event.preventDefault();
      if (sending || !draft.trim()) return;
      // Sent while the diagnostics are still being gathered, the report
      // would leave without what the box promised.
      if (kind === 'problem' && attach && draft_loading) return;
      const request = generation;
      const app = root.go && root.go.main && root.go.main.App;
      const params = { kind, message: draft };
      if (kind === 'problem' && attach && problem_draft) params.draft = problem_draft.id;
      sending = true; submit.disabled = true; select.disabled = true; message.disabled = true;
      submit.textContent = 'Отправляем…'; status.textContent = ''; last_status = '';
      render_attach();
      try {
        if (!app || typeof app.Feedback !== 'function') throw new Error('unavailable');
        const result = await app.Feedback(params);
        if (request !== generation) return;
        if (!result || result.error || !result.feedback || result.feedback.accepted !== true) throw new Error('failed');
        draft = ''; message.value = ''; prefill = ''; problem_draft = null;
        last_status = 'Сообщение отправлено. Спасибо!' + (result.feedback.code ? ' Код сообщения: ' + result.feedback.code : '');
        status.textContent = last_status;
        if (params.draft && root.problem_reported) root.problem_reported();
        if (root.account_ui) root.account_ui.refresh();
      } catch (_) {
        if (request !== generation) return;
        // A draft can expire while the person writes; the next try takes
        // a fresh one.
        if (params.draft) { problem_draft = null; setTimeout(load_draft); }
        last_status = 'Не удалось отправить. Черновик сохранён — попробуйте ещё раз.';
        status.textContent = last_status;
      } finally {
        if (request === generation) {
          sending = false; submit.disabled = false; select.disabled = false; message.disabled = false;
          submit.textContent = 'Отправить';
          const visible_message = $('feedback-message');
          if (visible_message && visible_message !== message) {
            visible_message.value = draft; visible_message.disabled = false;
            $('feedback-kind').disabled = false;
            $('feedback-status').textContent = last_status;
            const visible_submit = $('help').querySelector('[type="submit"]');
            if (visible_submit) { visible_submit.disabled = false; visible_submit.textContent = 'Отправить'; }
          }
          render_attach();
        }
      }
    });
  }
  // report_problem opens the form for a problem the window noticed, with
  // the message written in advance. Words the person already typed stay;
  // only an earlier message written in advance is replaced. The
  // diagnostics are gathered afresh, for this moment.
  function report_problem(text) {
    kind = 'problem';
    if (!draft.trim() || draft === prefill) draft = text;
    prefill = text;
    last_status = '';
    if (!sending) problem_draft = null;
    report();
    const message = $('feedback-message');
    if (message) { message.focus(); message.setSelectionRange(message.value.length, message.value.length); }
  }
  function open() {
    $('help').replaceChildren(button('FAQ ›', faq), button('Сообщить о баге ›', report));
    $('help').firstElementChild.focus();
  }
  function reset() {
    generation++; account_data = null; draft = ''; kind = 'problem'; sending = false; last_status = '';
    attach = true; attach_apps = true; problem_draft = null; draft_loading = false; draft_failed = false; prefill = '';
    $('help').replaceChildren(); $('help').hidden = true;
    $('help-button').setAttribute('aria-expanded', 'false');
  }
  root.help_ui = { open, reset, report_problem, account: (value) => { account_data = value; } };
})(window);
