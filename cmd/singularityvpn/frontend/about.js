// The changelog the window shows under Settings -> About: what each version
// changed, in the words of the person using it. The newest comes first; the
// one without a date is what is built but not yet released. Every change a
// person can see or feel adds a line here in the same commit.
(function (root) {
  'use strict';
  const changes = [
    {
      version: 'Следующая версия',
      items: [
        'Каждая проверка связи теперь шлёт и UDP-пакеты — так, как ходят игры. Потери UDP видны в статистике и в журнале.',
        'Подключение, которое прошло только со второй попытки, считается потерей пакетов, а не задержкой: «1057 мс вместо 56 мс» больше не появляется.',
        'Одиночный плохой замер — «короткий провал», пока повторная проверка через 5 с его не подтвердит. Провалы подряд сворачиваются в одну строку, каждый виден при наведении.',
        'В журнале провалы — красным, события сети — синим.',
        'Служба почти не нагружает Windows: все сетевые интерфейсы проверяются один раз при запуске, дальше — только когда сеть действительно изменилась.',
        'Журнал переживает перезапуск: видно, когда служба запущена, остановлена или завершилась сбоем.',
        'Окно, прижатое к нижнему краю экрана, остаётся у края, когда меняет высоту. В узком окне журнал сжимается первым, а когда места почти нет — сворачивается.',
        'Профили: только что оставленный профиль больше не показывается занятым на другом устройстве; смена профиля видна в журнале как переподключение.',
        'Раз в день, пока VPN выключен, проверяется, какие сервисы доступны в этой сети. Список сервисов можно править.',
        'Аккаунт и тариф, справка, статистика соединения, редактор тем.',
        'Обновления скачиваются и с GitHub, если он отвечает.',
        'Раздел «О программе» со списком изменений.'
      ]
    },
    {
      version: '0.1.3', date: '2026-10-25',
      items: [
        'Переподключение по WireGuard и AmneziaWG больше не обрывается, когда Windows долго пересоздаёт адаптер, — без минутных зависаний и перебора протоколов.',
        'Сотни ложных «TLS Error» от защиты сервера больше не забивают журнал; ложный «ping-restart» убран.',
        'Проверки связи не идут одна за другой без паузы после медленной проверки.'
      ]
    },
    {
      version: '0.1.2', date: '2026-10-25',
      items: [
        'Подключение больше не грузит службу DNS-клиента Windows (было до 67% процессора): адаптер VPN каждый раз тот же, маршруты не сбрасываются целиком.'
      ]
    },
    {
      version: '0.1.1', date: '2026-10-24',
      items: [
        'Прежде чем переподключаться, проверка связи подтверждает потерю через второй, независимый адрес — меньше ложных переподключений.',
        'Журнал не повторяет технические записи проверок.',
        'Первое обновление через само приложение.'
      ]
    },
    {
      version: '0.1.0', date: '2026-10-21',
      items: [
        'Первый выпуск: окно и значок в трее, вход с устройства, подключение одной кнопкой.',
        'Три протокола — AmneziaWG, WireGuard и OpenVPN. Сервер выбирается автоматически или закрепляется вручную.',
        'Режимы VPN и Антизапрет; цели: быстрый маршрут, обход блокировок, анонимность, страна.',
        'Игры идут через сервер отдельно от остального трафика с первого пакета; карточки игр с пингом, определение матча, ускорение.',
        'Соединение следует за сменой сети, проверяет, что трафик действительно идёт, и при проблемах переходит на другой сервер.',
        'Проверка блокировок: что блокирует сеть и каким способом.',
        'Журнал операций, профили, темы.',
        'Установщик, автозапуск, восстановление подключения после перезагрузки; проверенные обновления с возвратом к прежней версии, если новая не встала.'
      ]
    }
  ];

  // render fills box: one heading per version, its changes beneath. A date
  // is a moment, so it reads as time passed and carries the full date as
  // its hint, like every other moment in the window.
  function render(box, ago) {
    box.textContent = '';
    for (const c of changes) {
      const head = document.createElement('div');
      head.className = 'about-version';
      const name = document.createElement('span');
      name.textContent = c.date ? 'Версия ' + c.version : c.version;
      head.append(name);
      if (c.date) {
        const when = document.createElement('span');
        when.className = 'about-when';
        const at = c.date + 'T12:00:00';
        when.dataset.time = at;
        when.dataset.tip = new Date(at).toLocaleDateString('ru-RU', { day: 'numeric', month: 'long', year: 'numeric' });
        when.textContent = ago(at);
        head.append(when);
      }
      const list = document.createElement('ul');
      list.className = 'about-items';
      for (const text of c.items) {
        const li = document.createElement('li');
        li.textContent = text;
        list.append(li);
      }
      box.append(head, list);
    }
  }

  if (typeof module !== 'undefined') module.exports = { changes };
  root.about_ui = { changes, render };
})(typeof window !== 'undefined' ? window : globalThis);
