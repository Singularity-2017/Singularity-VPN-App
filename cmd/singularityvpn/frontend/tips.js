(function (root) {
  'use strict';
  if (!root.document) return;

  const $ = (id) => document.getElementById(id);
  let tip_target = null;
  let tip_timer = 0;

  function show_tip(el) {
    const text = el && el.dataset && el.dataset.tip;
    if (!text || !el.isConnected) return;
    const tip = $("tip");
    if (!tip) return;
    tip.textContent = text;
    tip.hidden = false;
    const r = el.getBoundingClientRect();
    const t = tip.getBoundingClientRect();
    const left = Math.max(8, Math.min(r.left + r.width / 2 - t.width / 2, window.innerWidth - t.width - 8));
    let top = r.bottom + 8;
    if (top + t.height > window.innerHeight - 8) top = r.top - t.height - 8;
    tip.style.left = left + "px";
    tip.style.top = Math.max(8, top) + "px";
  }

  function hide_tip() {
    clearTimeout(tip_timer);
    tip_target = null;
    const tip = $("tip");
    if (tip) tip.hidden = true;
  }

  function tip_owner(node) {
    return node && node.closest ? node.closest("[data-tip]") : null;
  }

  function watch_tips() {
    document.addEventListener("pointerover", (e) => {
      const el = tip_owner(e.target);
      if (el === tip_target) return;
      hide_tip();
      if (!el) return;
      tip_target = el;
      tip_timer = setTimeout(() => show_tip(el), 350);
    });
    document.addEventListener("pointerout", (e) => { if (!e.relatedTarget) hide_tip(); });
    document.addEventListener("focusin", (e) => {
      const el = tip_owner(e.target);
      hide_tip();
      if (el && el.matches(":focus-visible")) { tip_target = el; show_tip(el); }
    });
    document.addEventListener("focusout", hide_tip);
    document.addEventListener("pointerdown", hide_tip);
    document.addEventListener("keydown", (e) => { if (e.key === "Escape") hide_tip(); });
    document.addEventListener("scroll", hide_tip, true);
  }

  root.tips_ui = { show: show_tip, hide: hide_tip, watch: watch_tips };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', watch_tips);
  else watch_tips();
})(typeof window !== 'undefined' ? window : globalThis);
