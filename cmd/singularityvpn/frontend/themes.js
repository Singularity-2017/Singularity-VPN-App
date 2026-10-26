/* Theme overrides are scoped to both the mode and the selected palette. */
(function (root) {
  "use strict";
  const palettes = {
    singularity: ["#4be3df", "#20262b", "#e8eef2"],
    graphite: ["#eef1f3", "#1c1d1f", "#eef1f3"],
    amber: ["#ffb547", "#1f1a15", "#f3ebe1"],
    berserk: ["#ff3b5c", "#121316", "#ece6e8"],
    toxin: ["#a6ff2e", "#0f120f", "#e6f2e0"],
    synthwave: ["#ff4fd8", "#150e22", "#f1e8ff"],
  };
  const modes = { normal: ["singularity", "graphite", "amber"], gamer: ["berserk", "toxin", "synthwave"] };
  const patterns = ["none", "molecules", "slashes", "grid", "orbits", "dots"];
  const default_patterns = { singularity: "orbits", graphite: "none", amber: "dots", berserk: "slashes", toxin: "molecules", synthwave: "grid" };
  function valid_name(value) { return typeof value === "string" && value.trim().length > 0 && value.trim().length <= 48 && !/[<>\x00-\x1f\x7f]/.test(value); }
  function valid_pair(mode, id) { return Object.hasOwn(modes, mode) && modes[mode].includes(id); }
  function validate(value) {
    const clean = {};
    if (!value || typeof value !== "object" || Array.isArray(value)) return clean;
    for (const key of ["accent", "background", "text"]) {
      if (typeof value[key] === "string" && /^#[0-9a-f]{6}$/i.test(value[key])) clean[key] = value[key].toLowerCase();
    }
    if (["round", "rounded-square", "square"].includes(value.shape)) clean.shape = value.shape;
    if (patterns.includes(value.pattern)) clean.pattern = value.pattern;
    return clean;
  }
  function create_store(storage) {
    const memory = new Map();
    function read(key) {
      if (memory.has(key)) return memory.get(key);
      try { return storage()?.getItem(key); } catch (_) { return null; }
    }
    function write(key, value) {
      memory.set(key, value);
      try { storage()?.setItem(key, value); } catch (_) { /* Session fallback remains available. */ }
    }
    function custom_themes(mode) {
      if (!Object.hasOwn(modes, mode)) return [];
      try {
        const values = JSON.parse(read("theme.custom.v1." + mode));
        if (!Array.isArray(values)) return [];
        const names = new Set(), ids = new Set();
        return values.filter((t) => {
          if (!t || !/^custom-[a-z0-9-]+$/.test(t.id) || !valid_name(t.name) || !valid_pair(mode, t.base)) return false;
          const name = t.name.trim().toLocaleLowerCase();
          if (names.has(name) || ids.has(t.id)) return false;
          names.add(name); ids.add(t.id); return true;
        }).map((t) => ({ id: t.id, name: t.name.trim(), base: t.base, snapshot: validate(t.snapshot), sharp: mode === "gamer", note: "Своя тема" }));
      } catch (_) { return []; }
    }
    function accepts(mode, id) { return valid_pair(mode, id) || custom_themes(mode).some((t) => t.id === id); }
    function settings(mode, id) {
      const custom = custom_themes(mode).find((t) => t.id === id);
      const base = custom?.base || (accepts(mode, id) ? id : modes[mode]?.[0]) || "singularity";
      const [accent, background, text] = palettes[base];
      return { accent, background, text, shape: mode === "gamer" ? "rounded-square" : "round", pattern: default_patterns[base], ...custom?.snapshot, ...api.overrides(mode, id) };
    }
    const api = {
      custom_themes, settings,
      base(mode, id) { return custom_themes(mode).find((t) => t.id === id)?.base || id; },
      save_as(mode, id, name, reserved_names = []) {
        if (!accepts(mode, id) || !valid_name(name)) return { error: "Введите имя от 1 до 48 символов без угловых скобок и управляющих символов." };
        name = name.trim();
        const existing = custom_themes(mode);
        const preset_names = { normal: ["Сингулярность", "Графит", "Янтарь"], gamer: ["Берсерк", "Токсин", "Синтвейв"] };
        if ([...modes[mode], ...preset_names[mode], ...reserved_names, ...existing.map((t) => t.name)].some((n) => n.toLocaleLowerCase() === name.toLocaleLowerCase())) return { error: "Тема с таким именем уже есть." };
        let number = 1;
        while (existing.some((t) => t.id === "custom-" + number)) number++;
        const saved = { id: "custom-" + number, name, base: api.base(mode, id), snapshot: { ...settings(mode, id) } };
        write("theme.custom.v1." + mode, JSON.stringify([...existing, saved]));
        api.select(mode, saved.id);
        return { id: saved.id };
      },
      selected(mode) {
        const id = read("theme." + mode);
        return accepts(mode, id) ? id : modes[mode]?.[0];
      },
      select(mode, id) { if (accepts(mode, id)) write("theme." + mode, id); },
      overrides(mode, id) {
        if (!accepts(mode, id)) return {};
        try { return validate(JSON.parse(read("theme.editor.v1." + mode + "." + id))); } catch (_) { return {}; }
      },
      update(mode, id, value) {
        if (accepts(mode, id)) write("theme.editor.v1." + mode + "." + id, JSON.stringify(validate(value)));
      },
      reset(mode, id) { this.update(mode, id, {}); },
    };
    return api;
  }
  const store = create_store(() => root.localStorage);
  function settings(mode, id) {
    return store.settings(mode, id);
  }
  function rgb(hex) { return hex.slice(1).match(/../g).map((part) => parseInt(part, 16)); }
  function blend(a, b, amount) {
    return "#" + rgb(a).map((v, i) => Math.round(v * (1 - amount) + rgb(b)[i] * amount).toString(16).padStart(2, "0")).join("");
  }
  function apply(mode, id) {
    const el = root.document.documentElement;
    const value = settings(mode, id);
    const custom = store.custom_themes(mode).some((t) => t.id === id) ? value : store.overrides(mode, id);
    el.dataset.shape = value.shape;
    el.dataset.pattern = value.pattern;
    el.dataset.theme = store.base(mode, id);
    const names = ["accent", "accent-rgb", "accent-ink", "ground", "surface", "inset", "line", "hover", "branch", "text", "muted", "hot"];
    for (const name of names) el.style.removeProperty("--" + name);
    const set = (name, color) => el.style.setProperty("--" + name, color);
    if (custom.accent) {
      set("accent", value.accent);
      set("accent-rgb", rgb(value.accent).join(","));
      const light = rgb(value.accent).reduce((sum, channel, i) => sum + channel * [0.2126, 0.7152, 0.0722][i], 0);
      set("accent-ink", light > 150 ? "#101418" : "#ffffff");
    }
    if (custom.background) {
      set("ground", value.background);
      set("surface", blend(value.background, value.text, 0.06));
      set("inset", blend(value.background, "#000000", 0.15));
      for (const name of ["line", "branch"]) set(name, blend(value.background, value.text, 0.22));
      set("hover", blend(value.background, value.text, 0.12));
    }
    if (custom.text) { set("text", value.text); set("hot", value.text); }
    if (custom.text || custom.background) set("muted", blend(value.background, value.text, 0.68));
  }
  function render(mode, id) {
    const value = settings(mode, id);
    for (const key of ["accent", "background", "text", "shape", "pattern"]) root.document.getElementById("theme-" + key).value = value[key];
  }
  function bind(get_mode, get_id, changed, show_level) {
    const doc = root.document;
    const input = doc.getElementById("theme-name");
    const name_fields = doc.getElementById("theme-name-fields");
    const save_button = doc.getElementById("theme-save");
    const save_status = doc.getElementById("theme-save-status");
    function close_name(restore_focus = true) {
      name_fields.hidden = true;
      save_button.setAttribute("aria-expanded", "false");
      input.value = "";
      input.removeAttribute("aria-invalid");
      save_status.textContent = "";
      if (restore_focus) save_button.focus();
    }
    doc.getElementById("theme-edit").addEventListener("click", () => {
      close_name(false);
      render(get_mode(), get_id());
      show_level("settings", "theme-editor");
      doc.getElementById("theme-editor-back").focus();
    });
    doc.getElementById("theme-editor-back").addEventListener("click", () => {
      close_name(false);
      show_level("settings", "theme-panel");
      doc.getElementById("theme-edit").focus();
    });
    for (const key of ["accent", "background", "text", "shape", "pattern"]) {
      doc.getElementById("theme-" + key).addEventListener("input", (event) => {
        const mode = get_mode(), id = get_id();
        store.update(mode, id, { ...store.overrides(mode, id), [key]: event.target.value });
        changed();
      });
    }
    save_button.addEventListener("click", () => {
      name_fields.hidden = false;
      save_button.setAttribute("aria-expanded", "true");
      input.focus();
    });
    function save_name() {
      const result = store.save_as(get_mode(), get_id(), input.value);
      save_status.textContent = result.error || "Тема сохранена";
      input.setAttribute("aria-invalid", result.error ? "true" : "false");
      if (result.error) input.focus();
      else { close_name(); save_status.textContent = "Тема сохранена"; changed(); }
    }
    doc.getElementById("theme-save-confirm").addEventListener("click", save_name);
    doc.getElementById("theme-save-cancel").addEventListener("click", () => close_name());
    name_fields.addEventListener("keydown", (event) => {
      if (event.key === "Escape") {
        event.preventDefault(); event.stopPropagation(); close_name();
      } else if (event.key === "Enter" && event.target === input && !event.isComposing) {
        event.preventDefault(); event.stopPropagation(); save_name();
      }
    });
    input.addEventListener("input", () => {
      input.removeAttribute("aria-invalid");
      save_status.textContent = "";
    });
    doc.getElementById("theme-reset").addEventListener("click", () => {
      store.reset(get_mode(), get_id());
      changed();
    });
  }
  const api = { validate, create_store, store, settings, apply, render, bind };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  else root.theme_editor = api;
})(globalThis);
