/* Public emote catalogs are optional: playback and plain-text chat work offline. */
globalThis.ChatEmotes = (() => {
  const catalogs = new Map();
  const validID = (value) => typeof value === "string" && /^[a-zA-Z0-9_-]{1,256}$/.test(value);
  const imageHosts = new Set(["static-cdn.jtvnw.net", "cdn.betterttv.net", "cdn.frankerfacez.com", "cdn.7tv.app"]);

  function imageURL(value) {
    try {
      const url = new URL(value.startsWith("//") ? `https:${value}` : value);
      return url.protocol === "https:" && imageHosts.has(url.hostname) && !url.username && !url.password && !url.port ? url.href : null;
    } catch { return null; }
  }

  function json(url) {
    if (!catalogs.has(url)) catalogs.set(url, Promise.resolve().then(async () => {
      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), 6000);
      try {
        const response = await fetch(url, { signal: controller.signal, credentials: "omit", referrerPolicy: "no-referrer" });
        if (response.status === 404) return null; // A channel may not use this provider.
        if (!response.ok) throw new Error("Emote catalog unavailable");
        const text = await response.text();
        if (text.length > 4_000_000) throw new Error("Emote catalog too large");
        return JSON.parse(text);
      } catch {
        catalogs.delete(url); // Retry only failed providers when chat is opened again.
        return null;
      } finally { clearTimeout(timer); }
    }));
    return catalogs.get(url);
  }

  function add(map, code, url, provider) {
    const safe = typeof url === "string" && imageURL(url);
    if (typeof code === "string" && code.length <= 200 && safe) map.set(code, { url: safe, provider });
  }

  function twitch(map, value) {
    if (Array.isArray(value)) value.forEach((item) => twitch(map, item));
    else if (value && typeof value === "object") {
      if (validID(value.id) && typeof value.code === "string") {
        add(map, value.code, `https://static-cdn.jtvnw.net/emoticons/v2/${value.id}/default/dark/2.0`, "Twitch");
      } else Object.values(value).forEach((item) => twitch(map, item));
    }
  }

  function bttv(map, value) {
    const items = Array.isArray(value) ? value : [...(value?.channelEmotes || []), ...(value?.sharedEmotes || [])];
    for (const item of items) if (validID(item.id)) add(map, item.code, `https://cdn.betterttv.net/emote/${item.id}/2x`, "BetterTTV");
  }

  function ffz(map, value, global = false) {
    const sets = global ? (value?.default_sets || []).map((id) => value.sets?.[id]) : Object.values(value?.sets || {});
    for (const set of sets) for (const item of set?.emoticons || []) {
      add(map, item.name, item.animated?.["2"] || item.urls?.["2"] || item.urls?.["1"], "FrankerFaceZ");
    }
  }

  function sevenTV(map, value) {
    for (const item of value?.emotes || value?.emote_set?.emotes || []) {
      const host = item.data?.host;
      const file = host?.files?.find((file) => file.name === "2x.webp") || host?.files?.find((file) => /\.webp$/.test(file.name));
      if (file) add(map, item.name, `${host.url}/${file.name}`, "7TV");
    }
  }

  async function globalEmotes() {
    const values = await Promise.all([
      json("https://api.ivr.fi/v2/twitch/emotes/sets?set_id=0"),
      json("https://api.frankerfacez.com/v1/set/global"),
      json("https://api.betterttv.net/3/cached/emotes/global"),
      json("https://7tv.io/v3/emote-sets/global")
    ]);
    const map = new Map();
    twitch(map, values[0]); ffz(map, values[1], true); bttv(map, values[2]); sevenTV(map, values[3]);
    return map;
  }

  async function load(channel, channelID) {
    if (!/^[a-z0-9_]{1,25}$/.test(channel)) return new Map();
    try {
      const [global, native, franker, users] = await Promise.all([
        globalEmotes(), json(`https://api.ivr.fi/v2/twitch/emotes/channel/${channel}`),
        json(`https://api.frankerfacez.com/v1/room/${channel}`),
        /^\d+$/.test(channelID || "") ? null : json(`https://api.ivr.fi/v2/twitch/user?login=${channel}`)
      ]);
      const map = new Map(global);
      twitch(map, native); ffz(map, franker);
      const id = /^\d+$/.test(channelID || "") ? channelID : users?.[0]?.id || franker?.room?.twitch_id;
      if (/^\d+$/.test(String(id || ""))) {
        const [better, seven] = await Promise.all([
          json(`https://api.betterttv.net/3/cached/users/twitch/${id}`),
          json(`https://7tv.io/v3/users/twitch/${id}`)
        ]);
        bttv(map, better); sevenTV(map, seven);
      }
      return map;
    } catch { return new Map(); }
  }

  function parts(message, catalog = new Map()) {
    let text = message.message || "";
    if (text.startsWith("\u0001ACTION ")) text = text.slice(8).replace(/\u0001$/, "");
    const chars = Array.from(text);
    const ranges = [];
    for (const tag of (typeof message.emotes === "string" ? message.emotes : "").split("/")) {
      const [id, positions] = tag.split(":");
      if (!validID(id)) continue;
      for (const position of (positions || "").split(",")) {
        const match = /^(\d+)-(\d+)$/.exec(position);
        if (!match) continue;
        const start = Number(match[1]), end = Number(match[2]);
        if (start <= end && end < chars.length) ranges.push({ start, end, id });
      }
    }
    ranges.sort((a, b) => a.start - b.start);
    const output = [];
    const plain = (text) => {
      for (const word of text.split(/(\s+)/u)) if (word) output.push({ text: word, ...catalog.get(word) });
    };
    let cursor = 0;
    for (const range of ranges) {
      if (range.start < cursor) continue;
      plain(chars.slice(cursor, range.start).join(""));
      output.push({ text: chars.slice(range.start, range.end + 1).join(""), url: `https://static-cdn.jtvnw.net/emoticons/v2/${range.id}/default/dark/2.0`, provider: "Twitch" });
      cursor = range.end + 1;
    }
    plain(chars.slice(cursor).join(""));
    return output;
  }

  function render(element, message, catalog) {
    for (const part of parts(message, catalog)) {
      if (!part.url || !imageURL(part.url)) {
        element.append(document.createTextNode(part.text));
        continue;
      }
      const image = document.createElement("img");
      image.className = "chat-emote";
      image.src = part.url;
      image.alt = part.text;
      image.title = `${part.text} · ${part.provider}`;
      image.loading = "lazy";
      image.decoding = "async";
      image.referrerPolicy = "no-referrer";
      image.addEventListener("error", () => image.replaceWith(document.createTextNode(part.text)), { once: true });
      element.append(image);
    }
  }

  return { load, parts, render, imageURL };
})();
if (typeof module !== "undefined") module.exports = globalThis.ChatEmotes;
