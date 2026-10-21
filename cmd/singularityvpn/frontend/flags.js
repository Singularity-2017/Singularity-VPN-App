// Country flags and country names, for the server chip and the server menu.
//
// The flags and the name table are the control plane's, from its
// web/shared/flags.js, where the client portal and the admin panel draw the
// same countries. They are copied rather than loaded because this page talks
// to nobody but the service. A country learned there and not here would be a
// flag on one screen and two letters on the next: change both together.
//
// A flag is drawn, not spelled. Windows ships no flag glyphs, so the emoji
// built from a country code renders as the two letters it is made of. Only
// flags that plain rectangles render faithfully are described; any other
// country keeps its two-letter code, because a flag drawn wrong is worse than
// an honest code.
(function () {
  "use strict";

  const flag_w = 21, flag_h = 15;
  const flag_specs = {
    // horizontal bands, top to bottom
    DE: ["h", "#000000", "#DD0000", "#FFCE00"],
    NL: ["h", "#AE1C28", "#FFFFFF", "#21468B"],
    RU: ["h", "#FFFFFF", "#0039A6", "#D52B1E"],
    AT: ["h", "#ED2939", "#FFFFFF", "#ED2939"],
    EE: ["h", "#0072CE", "#000000", "#FFFFFF"],
    LT: ["h", "#FDB913", "#006A44", "#C1272D"],
    HU: ["h", "#CE2939", "#FFFFFF", "#477050"],
    BG: ["h", "#FFFFFF", "#00966E", "#D62612"],
    LU: ["h", "#ED2939", "#FFFFFF", "#00A1DE"],
    UA: ["h", "#0057B7", "#FFD700"],
    PL: ["h", "#FFFFFF", "#DC143C"],
    ID: ["h", "#FF0000", "#FFFFFF"],
    // vertical bands, hoist to fly
    FR: ["v", "#002395", "#FFFFFF", "#ED2939"],
    IT: ["v", "#009246", "#FFFFFF", "#CE2B37"],
    IE: ["v", "#169B62", "#FFFFFF", "#FF883E"],
    RO: ["v", "#002B7F", "#FCD116", "#CE1126"],
    BE: ["v", "#000000", "#FDDA24", "#EF3340"],
    // Nordic cross: field, cross, and an optional inner stripe
    SE: ["nordic", "#006AA7", "#FECC00"],
    DK: ["nordic", "#C8102E", "#FFFFFF"],
    FI: ["nordic", "#FFFFFF", "#003580"],
    NO: ["nordic", "#BA0C2F", "#FFFFFF", "#00205B"],
    IS: ["nordic", "#02529C", "#FFFFFF", "#DC1E35"],
    CH: ["cross", "#DA291C", "#FFFFFF"],
    JP: ["disc", "#FFFFFF", "#BC002D"],
    // stripes with a union in the corner: field, stripe, union
    US: ["canton", "#B22234", "#FFFFFF", "#3C3B6E"]
  };

  function rect(x, y, w, h, fill) {
    return '<rect x="' + x + '" y="' + y + '" width="' + w + '" height="' + h + '" fill="' + fill + '"/>';
  }

  // flag_svg returns inline SVG for a two-letter code, or "" for one it
  // cannot draw. Everything interpolated comes from flag_specs or from a
  // code already matched against /^[A-Z]{2}$/, so there is nothing to escape.
  function flag_svg(code) {
    const cc = String(code || "").trim().toUpperCase();
    if (!/^[A-Z]{2}$/.test(cc)) return "";
    const spec = flag_specs[cc];
    if (!spec) return "";
    const kind = spec[0];
    const colors = spec.slice(1);
    const round = (n) => +n.toFixed(3);
    let body = "";
    if (kind === "h") {
      const band = flag_h / colors.length;
      body = colors.map((c, i) => rect(0, round(i * band), flag_w, round(band), c)).join("");
    } else if (kind === "v") {
      const band = flag_w / colors.length;
      body = colors.map((c, i) => rect(round(i * band), 0, round(band), flag_h, c)).join("");
    } else if (kind === "nordic") {
      body = rect(0, 0, flag_w, flag_h, colors[0]) + rect(6, 0, 3, flag_h, colors[1]) + rect(0, 6, flag_w, 3, colors[1]);
      if (colors[2]) body += rect(7, 0, 1, flag_h, colors[2]) + rect(0, 7, flag_w, 1, colors[2]);
    } else if (kind === "cross") {
      body = rect(0, 0, flag_w, flag_h, colors[0]) + rect(8.5, 3, 4, 9, colors[1]) + rect(6, 5.5, 9, 4, colors[1]);
    } else if (kind === "canton") {
      // Thirteen stripes under a union seven stripes tall and two fifths of
      // the length; the fifty stars are dots on their real grid, because at
      // this size a five-pointed star is half a pixel across.
      const stripe = flag_h / 13;
      body = rect(0, 0, flag_w, flag_h, colors[0]);
      for (let i = 1; i < 13; i += 2) body += rect(0, round(i * stripe), flag_w, round(stripe), colors[1]);
      const union_w = round(flag_w * 0.4), union_h = round(stripe * 7);
      body += rect(0, 0, union_w, union_h, colors[2]);
      const dot = round(0.0308 * flag_h * Math.sqrt(1.1226 / Math.PI));
      for (let row = 0; row < 9; row++) {
        const wide = row % 2 === 0;
        for (let col = 0; col < (wide ? 6 : 5); col++) {
          const cx = round(union_w * ((wide ? 1 : 2) + col * 2) / 12);
          const cy = round(union_h * (row + 1) / 10);
          body += '<circle cx="' + cx + '" cy="' + cy + '" r="' + dot + '" fill="' + colors[1] + '"/>';
        }
      }
    } else if (kind === "disc") {
      body = rect(0, 0, flag_w, flag_h, colors[0]) + '<circle cx="10.5" cy="7.5" r="4.5" fill="' + colors[1] + '"/>';
    }
    return '<svg class="flag" viewBox="0 0 ' + flag_w + " " + flag_h + '" width="' + flag_w + '" height="' + flag_h +
      '" role="img" aria-label="' + cc + '">' + body + "</svg>";
  }

  // A node's country is free text an operator typed: one node says "DE",
  // another "Germany". Both have to land in the same group of the menu.
  const country_codes = {};
  const names_of = {};
  [
    ["SE", "sweden", "швеция"], ["NO", "norway", "норвегия"], ["FI", "finland", "финляндия"],
    ["DK", "denmark", "дания"], ["DE", "germany", "германия"], ["NL", "netherlands", "нидерланды", "голландия"],
    ["FR", "france", "франция"], ["GB", "united kingdom", "great britain", "britain", "англия", "великобритания"],
    ["PL", "poland", "польша"], ["CZ", "czechia", "czech republic", "чехия"], ["AT", "austria", "австрия"],
    ["CH", "switzerland", "швейцария"], ["IT", "italy", "италия"], ["ES", "spain", "испания"],
    ["PT", "portugal", "португалия"], ["RO", "romania", "румыния"], ["LV", "latvia", "латвия"],
    ["LT", "lithuania", "литва"], ["EE", "estonia", "эстония"], ["IS", "iceland", "исландия"],
    ["RU", "russia", "россия"], ["BY", "belarus", "беларусь", "белоруссия"], ["KZ", "kazakhstan", "казахстан"],
    ["UA", "ukraine", "украина"], ["TR", "turkey", "турция"], ["AM", "armenia", "армения"],
    ["GE", "georgia", "грузия"], ["MD", "moldova", "молдова"], ["US", "usa", "united states", "сша"],
    ["CA", "canada", "канада"], ["JP", "japan", "япония"], ["SG", "singapore", "сингапур"],
    ["HK", "hong kong", "гонконг"], ["AE", "uae", "united arab emirates", "оаэ"],
    ["IL", "israel", "израиль"], ["IN", "india", "индия"]
  ].forEach((entry) => {
    const code = entry[0];
    names_of[code] = entry.slice(1);
    entry.slice(1).forEach((name) => { country_codes[name] = code; });
  });

  // country_code turns whatever is in the country field into an ISO code,
  // or "" when it cannot. It never guesses from a prefix.
  function country_code(raw) {
    const s = String(raw || "").trim();
    if (!s) return "";
    if (/^[A-Za-z]{2}$/.test(s)) return s.toUpperCase();
    return country_codes[s.toLowerCase()] || "";
  }

  // names_for lists the spellings of a country this table knows, in lower
  // case: what a server's name may start with when it repeats its country.
  function names_for(code) {
    return names_of[code] || [];
  }

  window.flags = { flag_svg, country_code, names_for };
})();
