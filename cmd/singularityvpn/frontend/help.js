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
      ['Какой протокол выбрать?', 'Режим маршрутизации и протокол — разные настройки. Сервис поддерживает WireGuard, AmneziaWG и OpenVPN; приложение получает параметры туннеля от сервиса. Инструкции личного кабинета по ручному скачиванию профилей относятся к отдельным клиентам этих протоколов.'],
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
      ['Как сообщить о проблеме или предложить улучшение?', 'Помощь → Сообщить о баге. Выберите «Баг» или «Идея», опишите действия, ожидаемый результат и что произошло; для проблемы соединения укажите время и причину из журнала. Не отправляйте пароли или ключи. При ошибке отправки текст остаётся в форме для повторной попытки, а успешная отправка подтверждается сообщением.'],
      ['Как сохранить свою тему?', 'Настройки → Тема → Изменить. Цвета, форма и узор применяются сразу. Нажмите «Сохранить как», введите уникальное имя и нажмите «Сохранить» или Enter. «Отмена» или Escape закрывает ввод без создания темы. Обычный и игровой режимы имеют отдельный выбор темы.'],
      ['Где посмотреть отправленные сообщения?', 'Аккаунт → Вклад в сеть → История обратной связи. Там отображаются сообщения, результаты разбора и оценки, которые вернул сервис.'],
    ]) {
      const detail = make('details');
      detail.append(make('summary', question), make('p', answer));
      body.append(detail);
    }
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
    select.addEventListener('change', () => { kind = select.value; });
    const message_label = make('label', 'Сообщение');
    const message = make('textarea');
    message.id = 'feedback-message'; message_label.htmlFor = message.id;
    message.required = true; message.maxLength = 2000; message.rows = 6; message.value = draft;
    message.disabled = sending;
    message.addEventListener('input', () => { draft = message.value; });
    const status = make('div', last_status, 'feedback-status');
    status.id = 'feedback-status';
    status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
    const submit = make('button', sending ? 'Отправляем…' : 'Отправить', 'plain');
    submit.type = 'submit'; submit.disabled = sending;
    form.append(label, select, message_label, message, submit, status);
    body.append(form);
    form.addEventListener('submit', async (event) => {
      event.preventDefault();
      if (sending || !draft.trim()) return;
      const request = generation;
      const app = root.go && root.go.main && root.go.main.App;
      sending = true; submit.disabled = true; select.disabled = true; message.disabled = true;
      submit.textContent = 'Отправляем…'; status.textContent = ''; last_status = '';
      try {
        if (!app || typeof app.Feedback !== 'function') throw new Error('unavailable');
        const result = await app.Feedback({kind, message: draft});
        if (request !== generation) return;
        if (!result || result.error || !result.feedback || result.feedback.accepted !== true) throw new Error('failed');
        draft = ''; message.value = '';
        last_status = 'Сообщение отправлено. Спасибо!' + (result.feedback.code ? ' Код сообщения: ' + result.feedback.code : '');
        status.textContent = last_status;
        if (root.account_ui) root.account_ui.refresh();
      } catch (_) {
        if (request !== generation) return;
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
        }
      }
    });
  }
  function open() {
    $('help').replaceChildren(button('FAQ ›', faq), button('Сообщить о баге ›', report));
    $('help').firstElementChild.focus();
  }
  function reset() {
    generation++; account_data = null; draft = ''; kind = 'problem'; sending = false; last_status = '';
    $('help').replaceChildren(); $('help').hidden = true;
    $('help-button').setAttribute('aria-expanded', 'false');
  }
  root.help_ui = { open, reset, account: (value) => { account_data = value; } };
})(window);
