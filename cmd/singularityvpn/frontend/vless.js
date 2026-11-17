// Profile exports stay beside their profile. Credentials never enter the DOM:
// Download_vless opens the native save dialog and writes the file in Go.
(() => {
  function append(row, profile, api) {
    if (typeof api.Vless_options !== "function" || typeof api.Download_vless !== "function") return;
    const disclosure = document.createElement("button");
    disclosure.className = "opt vless-disclosure";
    disclosure.textContent = "VLESS ›";
    disclosure.setAttribute("aria-expanded", "false");
    disclosure.setAttribute("aria-label", "VLESS · " + (profile.label || "Без имени"));
    const panel = document.createElement("div");
    panel.className = "vless-options";
    panel.hidden = true;
    function note(text) {
      const item = document.createElement("div");
      item.className = "pop-note";
      item.textContent = text;
      item.setAttribute("role", "status");
      panel.replaceChildren(item);
    }
    disclosure.addEventListener("click", async () => {
      if (disclosure.disabled) return;
      if (!panel.hidden && panel.children.length && panel.children[0].tagName === "BUTTON") {
        panel.hidden = true;
        disclosure.setAttribute("aria-expanded", "false");
        return;
      }
      disclosure.disabled = true;
      panel.hidden = false;
      disclosure.setAttribute("aria-expanded", "true");
      note("Проверяем доступность…");
      try {
        const answer = await api.Vless_options(profile.id);
        if (!answer || answer.error) { note("Доступность VLESS сейчас не проверить. Нажмите VLESS, чтобы повторить."); return; }
        const nodes = (Array.isArray(answer.nodes) ? answer.nodes : []).filter(n => n && n.vless_available === true && typeof n.node_id === "string" && n.node_id);
        if (!nodes.length) { note("VLESS пока недоступен для этого профиля. Нажмите VLESS, чтобы проверить снова."); return; }
        panel.replaceChildren();
        for (const node of nodes) {
          const button = document.createElement("button");
          button.className = "opt";
          button.textContent = "Создать и скачать VLESS · " + node.node_id;
          const status = document.createElement("span");
          status.setAttribute("role", "status");
          button.append(status);
          button.addEventListener("click", async () => {
            if (button.disabled) return;
            button.disabled = true;
            status.textContent = "";
            try {
              const result = await api.Download_vless(profile.id, node.node_id);
              if (!result || result.code || result.error) status.textContent = " Не удалось сохранить. Повторить";
              else if (result.message === "saved") status.textContent = " Файл сохранён";
            } catch (_) {
              status.textContent = " Не удалось сохранить. Повторить";
            } finally { button.disabled = false; }
          });
          panel.append(button);
        }
      } catch (_) {
        note("Доступность VLESS сейчас не проверить. Нажмите VLESS, чтобы повторить.");
      } finally { disclosure.disabled = false; }
    });
    row.append(disclosure, panel);
  }
  window.vless_ui = { append };
})();
