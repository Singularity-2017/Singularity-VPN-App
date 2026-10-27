// The changelog the window shows under Settings -> About: what each version
// changed, in the words of the person using it, grouped by what it touches.
// The newest comes first; the one without a date is built but not yet
// released. Every change a person can see or feel adds a line here in the
// same commit.
(function (root) {
  'use strict';
  const changes = [
    {
      groups: [
        ['Связь', [
          'Каждая проверка связи шлёт и UDP-пакеты — так, как ходят игры. Потери UDP видны в статистике и в журнале.',
          'Подключение со второй попытки считается потерей пакетов, а не задержкой: «1057 мс вместо 56 мс» больше не появляется.',
          'Одиночный плохой замер — «короткий провал», пока проверка через 5 с его не подтвердит.',
          'Раз в день, пока VPN выключен, проверяется, какие сервисы доступны в этой сети. Список сервисов можно править.'
        ]],
        ['Нагрузка на компьютер', [
          'Сетевые интерфейсы проверяются полностью один раз при запуске, дальше — только когда сеть действительно изменилась.',
          'Окно больше не дёргает защищённое хранилище при каждом обновлении.'
        ]],
        ['Журнал', [
          'Провалы — красным, события сети — синим.',
          'Провалы подряд сворачиваются в одну строку, каждый виден при наведении.',
          'Переживает перезапуск: видно, когда служба запущена, остановлена или завершилась сбоем.',
          'Смена профиля видна как переподключение.'
        ]],
        ['Окно', [
          'Окно у нижнего края экрана остаётся у края, когда меняет высоту.',
          'В узком окне журнал сжимается первым, а когда места почти нет — сворачивается.',
          'Раздел «О программе»: изменения по версиям и разделам, старые версии свёрнуты, текущая отмечена своим номером.',
          'Аккаунт и тариф, справка, статистика соединения, редактор тем.'
        ]],
        ['Профили', [
          'Только что оставленный профиль больше не показывается занятым на другом устройстве.'
        ]],
        ['Обновления', [
          'Скачиваются и с GitHub, если он отвечает.'
        ]]
      ]
    },
    {
      version: '0.1.3', date: '2026-10-25',
      groups: [
        ['Связь', [
          'Переподключение по WireGuard и AmneziaWG не обрывается, когда Windows долго пересоздаёт адаптер, — без минутных зависаний и перебора протоколов.',
          'Проверки связи не идут одна за другой без паузы после медленной проверки.'
        ]],
        ['Журнал', [
          'Сотни ложных «TLS Error» от защиты сервера больше его не забивают; ложный «ping-restart» убран.'
        ]]
      ]
    },
    {
      version: '0.1.2', date: '2026-10-25',
      groups: [
        ['Нагрузка на компьютер', [
          'Подключение больше не грузит службу DNS-клиента Windows (было до 67% процессора): адаптер VPN каждый раз тот же, маршруты не сбрасываются целиком.'
        ]]
      ]
    },
    {
      version: '0.1.1', date: '2026-10-24',
      groups: [
        ['Связь', [
          'Прежде чем переподключаться, проверка подтверждает потерю через второй, независимый адрес — меньше ложных переподключений.'
        ]],
        ['Журнал', ['Не повторяет технические записи проверок.']],
        ['Обновления', ['Первое обновление через само приложение.']]
      ]
    },
    {
      version: '0.1.0', date: '2026-10-21',
      groups: [
        ['Первый выпуск', [
          'Окно и значок в трее, вход с устройства, подключение одной кнопкой.',
          'Три протокола — AmneziaWG, WireGuard и OpenVPN. Сервер выбирается автоматически или закрепляется вручную.',
          'Режимы VPN и Антизапрет; цели: быстрый маршрут, обход блокировок, анонимность, страна.',
          'Соединение следует за сменой сети, проверяет, что трафик идёт, и при проблемах переходит на другой сервер.',
          'Проверка блокировок: что блокирует сеть и каким способом.',
          'Журнал операций, профили, темы.'
        ]],
        ['Игры', [
          'Идут через сервер отдельно от остального трафика с первого пакета.',
          'Карточки игр с пингом, определение матча, ускорение.'
        ]],
        ['Установка и обновления', [
          'Установщик, автозапуск, восстановление подключения после перезагрузки.',
          'Проверенные обновления с возвратом к прежней версии, если новая не встала.'
        ]]
      ]
    }
  ];

  // entries is the changelog as this build sees it. A build of a release
  // marks that release as current and has nothing unreleased to show; any
  // other build -- dev or dirty -- is the unreleased entry, named by the
  // version the build supplied. A build that supplied none names nothing.
  function entries(version) {
    const bare = (version || '').replace(/^v/, '');
    const released = changes.some(c => c.version && c.version === bare);
    return changes.filter(c => c.version || !released).map(c => {
      if (c.version) return { title: 'Версия ' + c.version, date: c.date, current: c.version === bare, groups: c.groups };
      return { title: bare ? 'Версия ' + bare : 'Не выпущена', current: !!bare, groups: c.groups };
    });
  }

  // render fills box with one fold per version and one per kind of change
  // inside it. The current version starts open and the rest closed; what
  // the person opens or closes stays so. A date is a moment, so it reads as
  // time passed with the date itself as its hint.
  function render(box, version, ago, store) {
    box.textContent = '';
    const open = (key, fallback) => { const v = store.get(key); return v === null ? fallback : v === '1'; };
    const fold = (key, fallback, summary) => {
      const d = document.createElement('details');
      d.open = open(key, fallback);
      d.addEventListener('toggle', () => store.set(key, d.open ? '1' : '0'));
      const s = document.createElement('summary');
      s.append(...summary);
      d.append(s);
      return d;
    };
    const span = (cls, text) => { const e = document.createElement('span'); e.className = cls; e.textContent = text; return e; };
    for (const e of entries(version)) {
      const head = [span('about-title', e.title)];
      if (e.current) head.push(span('about-current', 'текущая'));
      if (e.date) {
        const at = e.date + 'T12:00:00';
        const when = span('about-when', ago(at));
        when.dataset.time = at;
        when.dataset.tip = new Date(at).toLocaleDateString('ru-RU', { day: 'numeric', month: 'long', year: 'numeric' });
        head.push(when);
      }
      const v = fold('about.' + (e.date ? e.title : 'next'), e.current, head);
      v.className = 'about-version';
      for (const [kind, items] of e.groups) {
        const g = fold('about.' + (e.date ? e.title : 'next') + '.' + kind, false, [span('about-kind', kind), span('about-count', String(items.length))]);
        g.className = 'about-group';
        const list = document.createElement('ul');
        for (const text of items) {
          const li = document.createElement('li');
          li.textContent = text;
          list.append(li);
        }
        g.append(list);
        v.append(g);
      }
      box.append(v);
    }
  }

  if (typeof module !== 'undefined') module.exports = { changes, entries };
  root.about_ui = { changes, entries, render };
})(typeof window !== 'undefined' ? window : globalThis);
