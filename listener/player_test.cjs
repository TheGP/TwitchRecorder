const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { parseHTML } = require("linkedom");

const web = (name) => fs.readFileSync(path.join(__dirname, "web", name), "utf8");
const tick = () => new Promise(setImmediate);

async function player() {
  const { document, Element, Event } = parseHTML(web("index.html"));
  const recording = { name: "testdj-2026-11-16.ts", size: 1000, modified: "2026-11-16", has_chat: true, bookmarks: [] };
  const requests = [];
  for (const media of document.querySelectorAll("audio, video")) {
    media.currentTime = 0;
    media.paused = true;
    media.pause = () => { media.paused = true; };
    media.load = () => { media.currentTime = 0; };
    media.play = async () => { media.paused = false; media.dispatchEvent(new Event("playing")); };
    Object.defineProperty(media, "src", { get: () => media.getAttribute("src"), set: (value) => media.setAttribute("src", value) });
  }
  const fetch = async (url, options = {}) => {
    requests.push({ url, ...options });
    let value = {};
    if (url === "/api/recordings") value = { folder: "/test", recordings: [structuredClone(recording)] };
    else if (url.endsWith("/info")) value = { kind: "audio", duration: 120 };
    else if (url.includes("/bookmarks")) {
      if (options.method === "POST") recording.bookmarks.push({ ...JSON.parse(options.body), id: "saved-bookmark" });
      if (options.method === "DELETE") recording.bookmarks = [];
      value = { bookmarks: structuredClone(recording.bookmarks) };
    }
    return { ok: true, json: async () => value, text: async () => JSON.stringify(value) };
  };
  const context = vm.createContext({
    document, Element, Event, console, URL, AbortController, setTimeout, clearTimeout,
    setInterval: () => 0, window: { addEventListener() {} }, Image: class {},
    localStorage: { getItem: () => null, setItem() {} }, fetch
  });
  vm.runInContext(web("chat-emotes.js"), context);
  vm.runInContext(web("app.js"), context);
  await tick();
  const run = (code) => vm.runInContext(code, context);
  await run(`openRecording(${JSON.stringify(recording.name)}, { shouldPlay: false, start: 10 })`);
  return { document, run, requests, context, recording, Event };
}

test("30-second buttons clamp to the recording and preserve paused/playing state", async () => {
  const { document, run } = await player();
  assert.equal(document.getElementById("skip-forward").disabled, false);
  document.getElementById("skip-back").click();
  assert.equal(run("view.start"), 0);
  assert.equal(run("view.media.paused"), true);
  document.getElementById("skip-forward").click();
  assert.equal(run("view.start"), 30);
  assert.equal(run("view.media.paused"), true);
  run("view.media.paused = false; view.media.currentTime = 85");
  document.getElementById("skip-forward").click();
  await tick();
  assert.equal(run("view.start"), 119.5);
  assert.equal(run("view.media.paused"), false);
});

test("bookmark button saves the absolute time and note; dots jump and removal updates both views", async () => {
  const { document, run, requests } = await player();
  run("view.start = 10; view.media.currentTime = 15");
  document.getElementById("bookmark-note").value = "Great song 🎵 <script>";
  document.getElementById("add-bookmark").click();
  await tick();
  const request = requests.find((r) => r.method === "POST" && r.url.endsWith("/bookmarks"));
  assert.deepEqual(JSON.parse(request.body), { seconds: 25, label: "Great song 🎵 <script>" });
  assert.equal(document.querySelectorAll(".bookmark-marker").length, 1);
  assert.match(document.querySelector(".bookmark-jump").textContent, /0:25 · Great song/);
  assert.equal(document.querySelectorAll("#bookmark-list script").length, 0);
  assert.equal(document.getElementById("bookmark-note").value, "");
  document.querySelector(".bookmark-marker").click();
  assert.equal(run("view.start"), 25);
  assert.equal(run("view.media.paused"), true);
  await run("refresh()");
  assert.equal(document.querySelectorAll(".bookmark-marker").length, 1);
  document.querySelector(".bookmark-remove").click();
  await tick();
  assert.equal(document.querySelectorAll(".bookmark-marker").length, 0);
  assert.equal(document.querySelectorAll("#bookmark-list li").length, 0);
});

test("chat emotes preserve Unicode, remain safe text, and do not restart earlier images for new messages", async () => {
  const { document, run } = await player();
  run(`view.chatVisible = true; view.chatName = view.current;
    view.chat = [
      {offset_seconds: 1, username: 'viewer', message: '🎵 Kappa', emotes: '25:2-6'},
      {offset_seconds: 2, username: 'viewer', message: '<img src=x onerror=alert(1)>'}
    ]; syncChat(1);`);
  const first = document.querySelector(".chat-message");
  const image = first.querySelector("img");
  assert.equal(image.alt, "Kappa");
  assert.match(image.src, /emoticons\/v2\/25\//);
  assert.match(first.textContent, /🎵/u);
  run("syncChat(2)");
  assert.equal(document.querySelector(".chat-message"), first);
  assert.equal(document.querySelectorAll(".chat-messages img").length, 1);
  assert.match(document.querySelectorAll(".chat-text")[1].textContent, /<img src=x/);
  run("syncChat(0)");
  assert.equal(document.querySelectorAll(".chat-message").length, 0);
  run(`view.chat = Array.from({length: 250}, (_, i) => ({offset_seconds: i, username: 'viewer', message: 'hi'})); syncChat(249);`);
  assert.equal(document.querySelectorAll(".chat-message").length, 200);
});

test("emote catalogs include native and plugin emotes, resolve unregistered FFZ channels, and are cached", async () => {
  const { context, run } = await player();
  const calls = [];
  context.fetch = async (url) => {
    calls.push(url);
    let value = null;
    if (url.includes("sets?set_id=0")) value = [{ emoteList: [{ id: "25", code: "Kappa" }] }];
    else if (url.includes("emotes/channel/")) value = { subProducts: [{ emotes: [{ id: "emotesv2_abc", code: "djDance" }] }] };
    else if (url.includes("user?login=")) value = [{ id: "123" }];
    else if (url.endsWith("cached/emotes/global")) value = [{ id: "global", code: "Dance" }];
    else if (url.includes("betterttv.net/3/cached/users/")) value = { sharedEmotes: [{ id: "channel", code: "Dance" }] };
    else if (url.endsWith("set/global")) value = { default_sets: [1], sets: { 1: { emoticons: [{ name: "FFZ", urls: { 2: "//cdn.frankerfacez.com/emoticon/1/2" } }] } } };
    else if (url.endsWith("emote-sets/global")) value = { emotes: [{ name: "Seven", data: { host: { url: "//cdn.7tv.app/emote/abc", files: [{ name: "2x.webp" }] } } }] };
    return { ok: value !== null, status: value !== null ? 200 : 404, text: async () => JSON.stringify(value) };
  };
  const map = await run("ChatEmotes.load('testdj')");
  assert.equal(map.get("Kappa").provider, "Twitch");
  assert.equal(map.get("djDance").provider, "Twitch");
  assert.equal(map.get("Dance").url, "https://cdn.betterttv.net/emote/channel/2x");
  assert.equal(map.get("FFZ").provider, "FrankerFaceZ");
  assert.equal(map.get("Seven").provider, "7TV");
  const callsBefore = calls.length;
  assert.deepEqual([...(await run("ChatEmotes.load('testdj')"))], [...map]);
  assert.equal(calls.length, callsBefore);
  assert.equal(calls.filter((url) => url.includes("user?login=")).length, 1);
  assert.equal(run("ChatEmotes.imageURL('javascript:alert(1)')"), null);
  assert.equal(run("ChatEmotes.imageURL('https://attacker.example/image.png')"), null);
  assert.equal(run("ChatEmotes.imageURL('https://user@cdn.7tv.app/image.png')"), null);
  context.fetch = async () => { throw new Error("offline"); };
  assert.equal((await run("ChatEmotes.load('offlinechannel')")).get("Kappa").provider, "Twitch");
  const parts = run("ChatEmotes.parts({message: '🎵 Kappa hi', emotes: '25:2-6/invalid:999-1000'})");
  assert.equal(parts.find((part) => part.url)?.text, "Kappa");
  assert.equal(parts.map((part) => part.text).join(""), "🎵 Kappa hi");
});

test("failed global and channel emote lookups retry after connectivity returns", async () => {
  const { context, run } = await player();
  let online = false;
  const calls = [];
  context.fetch = async (url) => {
    calls.push(url);
    if (!online) throw new Error("offline");
    const value = url.includes("emotes/channel/")
      ? { emotes: [{ id: "emotesv2_dance", code: "djDance" }] }
      : [{ emoteList: [{ id: "25", code: "Kappa" }] }];
    return { ok: true, status: 200, text: async () => JSON.stringify(value) };
  };
  assert.equal((await run("ChatEmotes.load('testdj', '123')")).size, 0);
  const callsWhileOffline = calls.length;
  online = true;
  const recovered = await run("ChatEmotes.load('testdj', '123')");
  assert.equal(recovered.get("Kappa").provider, "Twitch");
  assert.equal(recovered.get("djDance").provider, "Twitch");
  assert.ok(calls.length > callsWhileOffline);
});

test("a failing emote provider retries without refetching healthy providers or missing channels", async () => {
  const { context, run } = await player();
  const calls = new Map();
  let recovered = false;
  const failing = "https://api.betterttv.net/3/cached/emotes/global";
  const healthy = "https://api.ivr.fi/v2/twitch/emotes/sets?set_id=0";
  const missing = "https://api.frankerfacez.com/v1/room/testdj";
  context.fetch = async (url) => {
    calls.set(url, (calls.get(url) || 0) + 1);
    if (url === failing && !recovered) return { ok: false, status: 503 };
    let value = null;
    if (url === healthy) value = [{ emoteList: [{ id: "25", code: "Kappa" }] }];
    if (url === failing) value = [{ id: "dance", code: "Dance" }];
    return { ok: value !== null, status: value !== null ? 200 : 404, text: async () => JSON.stringify(value) };
  };
  const first = await run("ChatEmotes.load('testdj', '123')");
  assert.equal(first.get("Kappa").provider, "Twitch");
  assert.equal(first.has("Dance"), false);
  recovered = true;
  const second = await run("ChatEmotes.load('testdj', '123')");
  assert.equal(second.get("Dance").provider, "BetterTTV");
  assert.equal(calls.get(failing), 2);
  assert.equal(calls.get(healthy), 1);
  assert.equal(calls.get(missing), 1);
});
