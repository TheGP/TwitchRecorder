const el = Object.fromEntries([
  "folder", "total-count", "unwatched-count", "watched-count", "refresh",
  "search", "sort", "select-visible", "selection-count", "mark-selected",
  "unmark-selected", "mark-all", "list", "list-empty",
  "player-panel", "artwork", "avatar", "avatar-fallback", "player-body", "video", "audio", "player-title", "player-subtitle",
  "seek", "elapsed", "duration", "play", "volume",
  "fullscreen", "player-watched", "notice"
].map((id) => [id, document.getElementById(id)]));

const localStateKey = "twitch-listener-player-v1";
let videoClickTimer;
const view = {
  recordings: [], selected: new Set(), filter: "all", current: null,
  info: null, media: null, start: 0, seeking: false, loading: false,
  playing: false, requestedPlay: false, retryCount: 0, streamVersion: 0,
  leaving: false, lastProgressSave: 0, lastLocalSave: 0
};

async function api(path, options = {}) {
  const response = await fetch(path, options);
  if (!response.ok) {
    const message = (await response.text()).trim();
    throw new Error(message || `Request failed (${response.status})`);
  }
  return response.json();
}

function notify(message) { el.notice.textContent = message || ""; }

function readLocalState() {
  try {
    return JSON.parse(localStorage.getItem(localStateKey));
  } catch {
    return null;
  }
}

function saveLocalState(force = false, seconds = view.start + (view.media?.currentTime || 0)) {
  if (!force && Date.now() - view.lastLocalSave < 1000) return;
  view.lastLocalSave = Date.now();
  try {
    localStorage.setItem(localStateKey, JSON.stringify({
      name: view.current,
      position: Number.isFinite(seconds) ? seconds : 0,
      playing: view.playing,
      volume: Number(el.volume.value),
      search: el.search.value, sort: el.sort.value, filter: view.filter
    }));
  } catch {
    // Playback still works when browser storage is unavailable.
  }
}

function formatTime(value) {
  if (!Number.isFinite(value) || value < 0) return "0:00";
  const seconds = Math.floor(value);
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const rest = String(seconds % 60).padStart(2, "0");
  return hours ? `${hours}:${String(minutes).padStart(2, "0")}:${rest}` : `${minutes}:${rest}`;
}

function formatSize(bytes) {
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} GB`;
  return `${(bytes / 1024 ** 2).toFixed(0)} MB`;
}

function recordingParts(name) {
  const match = name.match(/^(.*)-(\d{4}-\d{2}-\d{2}|\d{2}-\d{2}-\d{4})(?:-(\d+))?\.ts$/i);
  return match
    ? { channel: match[1], date: match[2] }
    : { channel: name.replace(/\.ts$/i, ""), date: "" };
}

function formatRecordingDate(date) {
  const [first, month, last] = date.split("-");
  return first.length === 4 ? `${last}.${month}.${first}` : `${first}.${month}.${last}`;
}

function visibleRecordings() {
  const query = el.search.value.trim().toLowerCase();
  const files = view.recordings.filter((file) => {
    if (view.filter === "watched" && !file.watched) return false;
    if (view.filter === "unwatched" && file.watched) return false;
    return file.name.toLowerCase().includes(query);
  });
  if (el.sort.value === "name") {
    files.sort((a, b) => a.name.localeCompare(b.name));
  } else {
    files.sort((a, b) => new Date(a.modified) - new Date(b.modified));
    if (el.sort.value === "newest") files.reverse();
  }
  return files;
}

function renderStats() {
  const watched = view.recordings.filter((file) => file.watched).length;
  el["total-count"].textContent = view.recordings.length;
  el["watched-count"].textContent = watched;
  el["unwatched-count"].textContent = view.recordings.length - watched;
}

function renderList() {
  const files = visibleRecordings();
  el.list.replaceChildren();
  el["list-empty"].hidden = files.length !== 0;
  for (const file of files) {
    const { channel, date } = recordingParts(file.name);
    const isPlaying = view.current === file.name && view.media && !view.media.paused && !view.media.ended;
    const row = document.createElement("div");
    row.className = "recording-row" + (view.current === file.name ? " active" : "") + (file.watched ? " watched" : "");
    row.setAttribute("role", "button");
    row.setAttribute("tabindex", "0");
    row.setAttribute("aria-label", `${isPlaying ? "Pause" : "Play"} ${file.name}, ${file.watched ? "watched" : "unwatched"}`);
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.className = "row-check";
    checkbox.checked = view.selected.has(file.name);
    checkbox.setAttribute("aria-label", `Select ${file.name}`);
    checkbox.addEventListener("click", (event) => event.stopPropagation());
    checkbox.addEventListener("change", () => {
      if (checkbox.checked) view.selected.add(file.name);
      else view.selected.delete(file.name);
      renderSelection();
    });
    const icon = document.createElement("div");
    icon.className = "row-play";
    icon.textContent = isPlaying ? "Ⅱ" : "▶";
    icon.setAttribute("aria-hidden", "true");
    const main = document.createElement("div");
    main.className = "row-main";
    const title = document.createElement("div");
    title.className = "row-title";
    title.textContent = channel;
    title.title = file.name;
    const meta = document.createElement("div");
    meta.className = "row-meta";
    meta.textContent = `${file.name} · ${formatSize(file.size)}`;
    meta.title = file.name;
    main.append(title, meta);
    const recordingDate = document.createElement("span");
    recordingDate.className = "row-date";
    recordingDate.textContent = date;
    const dot = document.createElement("span");
    dot.className = "unwatched-dot" + (file.watched ? " is-invisible" : "");
    dot.title = file.watched ? "" : "Unwatched";
    dot.setAttribute("aria-hidden", "true");
    row.append(dot, checkbox, icon, main, recordingDate);
    const deleteButton = document.createElement("button");
    deleteButton.type = "button";
    deleteButton.className = "row-delete";
    deleteButton.title = `Delete ${file.name}`;
    deleteButton.setAttribute("aria-label", `Delete ${file.name}`);
    deleteButton.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 7h16M10 4h4M6 7l1 13h10l1-13M10 11v6M14 11v6"/></svg>';
    deleteButton.addEventListener("click", async (event) => {
      event.stopPropagation();
      deleteButton.disabled = true;
      try {
        await deleteRecording(file.name);
      } finally {
        deleteButton.disabled = false;
      }
    });
    row.append(deleteButton);
    row.addEventListener("click", () => openRecording(file.name));
    row.addEventListener("keydown", (event) => {
      if (event.target === row && (event.key === "Enter" || event.key === " ")) {
        event.preventDefault();
        openRecording(file.name);
      }
    });
    el.list.append(row);
  }
  renderSelection();
}

function renderSelection() {
  const visible = visibleRecordings();
  const chosen = visible.filter((file) => view.selected.has(file.name)).length;
  el["selection-count"].textContent = `${view.selected.size} selected`;
  el["mark-selected"].disabled = view.selected.size === 0;
  el["unmark-selected"].disabled = view.selected.size === 0;
  el["mark-all"].disabled = view.recordings.length === 0;
  el["select-visible"].checked = visible.length > 0 && chosen === visible.length;
  el["select-visible"].indeterminate = chosen > 0 && chosen < visible.length;
}

async function refresh() {
  try {
    const result = await api("/api/recordings");
    view.recordings = result.recordings;
    el.folder.textContent = result.folder;
    el.folder.title = result.folder;
    const names = new Set(view.recordings.map((file) => file.name));
    for (const selected of view.selected) if (!names.has(selected)) view.selected.delete(selected);
    renderStats();
    renderList();
    updateWatchedButton();
    notify("");
  } catch (error) {
    notify(`Library unavailable: ${error.message}`);
  }
}

async function setWatched(names, watched, all = false) {
  try {
    await api("/api/watched", {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ names, watched, all })
    });
    view.selected.clear();
    await refresh();
  } catch (error) {
    notify(`Could not save watched status: ${error.message}`);
  }
}

async function deleteRecording(name) {
  if (!window.confirm(`Permanently delete ${name} from ${el.folder.textContent}?`)) return;
  const wasCurrent = view.current === name;
  const position = view.start + (view.media?.currentTime || 0);
  if (wasCurrent) {
    if (document.fullscreenElement) await document.exitFullscreen();
    view.current = null;
    view.info = null;
    view.playing = false;
    stopMedia();
  }
  try {
    await api(`/api/recordings/${encodeURIComponent(name)}`, { method: "DELETE" });
    view.selected.delete(name);
    if (wasCurrent) {
      view.start = 0;
      view.loading = false;
      view.seeking = false;
      el["player-panel"].classList.add("is-empty");
      el["player-title"].textContent = "Pick a recording";
      el["player-subtitle"].textContent = "Select a file below to start listening or watching.";
      el.seek.value = "0";
      el.elapsed.textContent = "0:00";
      el.duration.textContent = "0:00";
      el.play.textContent = "▶";
      el.play.setAttribute("aria-label", "Play");
      el.fullscreen.hidden = true;
      for (const id of ["seek", "play", "player-watched"]) el[id].disabled = true;
      saveLocalState(true, 0);
    }
    await refresh();
    notify(`Deleted ${name}`);
  } catch (error) {
    if (wasCurrent) await openRecording(name, { start: position, shouldPlay: false });
    notify(`Could not delete ${name}: ${error.message}`);
  }
}

function stopMedia() {
  clearTimeout(videoClickTimer);
  view.streamVersion++;
  view.loading = false;
  view.media = null;
  el.avatar.hidden = true;
  el.avatar.removeAttribute("src");
  el["avatar-fallback"].hidden = true;
  for (const media of [el.video, el.audio]) {
    media.pause();
    media.removeAttribute("src");
    media.load();
    media.hidden = true;
  }
  el.artwork.hidden = true;
  el["player-body"].classList.remove("video-mode", "audio-mode");
}

async function loadAvatar(channel, name) {
  el["avatar-fallback"].hidden = false;
  try {
    const { url } = await api(`/api/avatars/${encodeURIComponent(channel)}`);
    if (!url || view.current !== name || view.info?.kind !== "audio") return;
    const image = new Image();
    image.onload = () => {
      if (view.current !== name || view.info?.kind !== "audio") return;
      el.avatar.src = url;
      el.avatar.alt = `${channel} profile picture`;
      el.avatar.hidden = false;
      el["avatar-fallback"].hidden = true;
    };
    image.src = url;
  } catch {
    // Keep the audio artwork fallback when Twitch is unavailable.
  }
}

function streamURL(start) {
  return `/api/recordings/${encodeURIComponent(view.current)}/stream?start=${start.toFixed(3)}`;
}

function saveProgress(force = false, seconds = view.start + (view.media?.currentTime || 0)) {
  if (!view.current || !view.info || !view.media) return;
  const file = view.recordings.find((item) => item.name === view.current);
  if (!file || file.watched || !Number.isFinite(seconds)) return;
  const position = Math.max(0, Math.min(seconds, view.info.duration));
  if (!force && (position < 1 || Date.now() - view.lastProgressSave < 10_000)) return;
  if (Math.abs((file.progress || 0) - position) < 0.5) return;
  file.progress = position;
  view.lastProgressSave = Date.now();
  api("/api/progress", {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name: file.name, seconds: position }),
    keepalive: true
  }).catch((error) => notify(`Could not save progress: ${error.message}`));
}

async function playAt(start, shouldPlay = true, savePosition = true, retry = false) {
  if (!view.current || !view.info || !view.media) return;
  const media = view.media;
  const version = ++view.streamVersion;
  if (!retry) view.retryCount = 0;
  view.start = Math.max(0, Math.min(start, Math.max(0, view.info.duration - 0.5)));
  view.playing = shouldPlay;
  view.requestedPlay = shouldPlay;
  view.loading = true;
  media.src = streamURL(view.start);
  media.volume = Number(el.volume.value);
  media.load();
  updateTimeline();
  saveLocalState(true, view.start);
  if (savePosition) saveProgress(true, view.start);
  if (shouldPlay) {
    try {
      await media.play();
    } catch (error) {
      if (version === view.streamVersion && error.name !== "AbortError" && !media.error) {
        view.playing = false;
        view.requestedPlay = false;
        saveLocalState(true);
        notify(error.name === "NotAllowedError" ? "Press play to resume." : `Playback could not start: ${error.message}`);
      }
    }
  }
  if (version === view.streamVersion) {
    view.loading = false;
    if (media.error) recoverMediaError(media);
  }
}

function recoverMediaError(media) {
  if (media !== view.media || !media.error || !view.info) return;
  const position = Math.min(view.info.duration - 0.5, view.start + (media.currentTime || 0));
  if (view.requestedPlay && view.retryCount === 0 && position + 2 < view.info.duration) {
    view.retryCount = 1;
    playAt(position + 2, true, false, true);
    return;
  }
  saveProgress(true, position);
  view.start = position;
  view.playing = false;
  view.requestedPlay = false;
  media.removeAttribute("src");
  media.load();
  updateTimeline();
  saveLocalState(true, position);
  el.play.textContent = "▶";
  el.play.setAttribute("aria-label", "Play");
  renderList();
  notify("Playback failed. Press play to retry, or select another recording.");
}

async function openRecording(name, options = {}) {
  if (view.current === name) {
    togglePlayback();
    return;
  }
  const { channel, date } = recordingParts(name);
  saveProgress(true);
  stopMedia();
  view.current = name;
  view.info = null;
  view.start = 0;
  view.playing = options.shouldPlay ?? true;
  view.requestedPlay = view.playing;
  view.retryCount = 0;
  view.lastProgressSave = 0;
  el["player-panel"].classList.remove("is-empty");
  for (const id of ["seek", "play", "player-watched"]) el[id].disabled = true;
  notify("Inspecting recording…");
  el["player-title"].textContent = date
    ? `${channel.charAt(0).toUpperCase()}${channel.slice(1)} · ${formatRecordingDate(date)}`
    : name;
  el["player-subtitle"].textContent = date ? name : "Loading media details";
  renderList();
  try {
    const info = await api(`/api/recordings/${encodeURIComponent(name)}/info`);
    if (view.current !== name) return;
    view.info = info;
    const hasVideo = info.kind === "video";
    view.media = hasVideo ? el.video : el.audio;
    el.video.hidden = !hasVideo;
    el.artwork.hidden = false;
    el["player-body"].classList.toggle("video-mode", hasVideo);
    el["player-body"].classList.toggle("audio-mode", !hasVideo);
    if (!hasVideo) loadAvatar(channel, name);
    if (!date) el["player-subtitle"].textContent = `${channel} · ${formatTime(info.duration)}`;
    el.duration.textContent = formatTime(info.duration);
    el.seek.max = String(Math.floor(info.duration));
    for (const id of ["seek", "play", "player-watched"]) el[id].disabled = false;
    el.fullscreen.hidden = info.kind !== "video";
    updateWatchedButton();
    notify("");
    const file = view.recordings.find((item) => item.name === name);
    const saved = file?.watched ? 0 : (options.start ?? file?.progress ?? 0);
    await playAt(saved > 0 && saved < info.duration - 1 ? saved : 0, view.playing, false);
  } catch (error) {
    if (view.current === name) {
      notify(`Cannot open recording: ${error.message}`);
    }
  }
}

function updateTimeline() {
  if (!view.info) return;
  const current = Math.min(view.info.duration, view.start + (view.media?.currentTime || 0));
  if (!view.seeking) el.seek.value = String(Math.floor(current));
  el.elapsed.textContent = formatTime(current);
}

function updateWatchedButton() {
  const file = view.recordings.find((item) => item.name === view.current);
  el["player-watched"].textContent = file?.watched ? "Mark unwatched" : "Mark watched";
}

for (const media of [el.video, el.audio]) {
  media.addEventListener("timeupdate", () => {
    if (media === view.media) {
      if (view.retryCount && media.currentTime > 10) view.retryCount = 0;
      updateTimeline();
      if (!view.loading) {
        saveProgress();
        saveLocalState();
      }
    }
  });
  media.addEventListener("playing", () => {
    if (media === view.media) {
      view.playing = true;
      el.play.textContent = "Ⅱ";
      el.play.setAttribute("aria-label", "Pause");
      saveLocalState(true);
      renderList();
      notify("");
    }
  });
  media.addEventListener("pause", () => {
    if (media === view.media) {
      el.play.textContent = "▶";
      el.play.setAttribute("aria-label", "Play");
      if (!view.loading && !view.leaving && !document.hidden) {
        view.playing = false;
        saveProgress(true);
        saveLocalState(true);
      }
      renderList();
    }
  });
  media.addEventListener("ended", async () => {
    if (media === view.media && media.ended) {
      const name = view.current;
      const reachedEnd = view.start + media.currentTime >= view.info.duration - 10;
      const visible = visibleRecordings();
      const index = visible.findIndex((item) => item.name === name);
      const nextName = index >= 0 ? visible.slice(index + 1).find((item) => !item.watched)?.name : null;
      view.playing = false;
      view.requestedPlay = false;
      saveLocalState(true, 0);
      updateTimeline();
      renderList();
      const file = view.recordings.find((item) => item.name === name);
      if (file && !file.watched && reachedEnd) {
        await setWatched([name], true);
      } else {
        saveProgress(true);
      }
      if (reachedEnd && nextName && view.current === name && !view.playing) await openRecording(nextName);
    }
  });
  media.addEventListener("error", () => {
    if (!view.loading) recoverMediaError(media);
  });
}

el.refresh.addEventListener("click", refresh);
el.search.addEventListener("input", () => { renderList(); saveLocalState(true); });
el.sort.addEventListener("change", () => { renderList(); saveLocalState(true); });
document.querySelectorAll(".filter").forEach((button) => {
  button.addEventListener("click", () => {
    view.filter = button.dataset.filter;
    document.querySelectorAll(".filter").forEach((other) => {
      other.classList.toggle("active", other === button);
      other.setAttribute("aria-pressed", other === button ? "true" : "false");
    });
    renderList();
    saveLocalState(true);
  });
});
el["select-visible"].addEventListener("change", (event) => {
  for (const file of visibleRecordings()) {
    if (event.target.checked) view.selected.add(file.name);
    else view.selected.delete(file.name);
  }
  renderList();
});
el["mark-selected"].addEventListener("click", () => setWatched([...view.selected], true));
el["unmark-selected"].addEventListener("click", () => setWatched([...view.selected], false));
el["mark-all"].addEventListener("click", () => setWatched([], true, true));
el["player-watched"].addEventListener("click", () => {
  const file = view.recordings.find((item) => item.name === view.current);
  if (file) setWatched([file.name], !file.watched);
});
function togglePlayback() {
  const media = view.media;
  if (!media) return;
  view.playing = media.paused;
  view.requestedPlay = media.paused;
  saveLocalState(true);
  if (media.paused && !media.getAttribute("src")) {
    playAt(Math.min(view.start + 2, view.info.duration - 0.5));
  } else if (media.paused) media.play().catch((error) => {
    view.playing = false;
    view.requestedPlay = false;
    saveLocalState(true);
    notify(error.message);
  });
  else media.pause();
}

async function toggleFullscreen() {
  try {
    if (document.fullscreenElement === el.video) await document.exitFullscreen();
    else await el.video.requestFullscreen();
  } catch (error) {
    notify(`Fullscreen failed: ${error.message}`);
  }
}

el.play.addEventListener("click", togglePlayback);
document.addEventListener("keydown", (event) => {
  if (event.key !== " " || event.repeat || event.altKey || event.ctrlKey || event.metaKey || !view.media) return;
  const target = event.target;
  if (target instanceof Element && (target.isContentEditable || target.closest("button, input, select, textarea, a, summary, [role='button']"))) return;
  event.preventDefault();
  togglePlayback();
});
el.video.addEventListener("click", () => {
  clearTimeout(videoClickTimer);
  const name = view.current;
  videoClickTimer = setTimeout(() => {
    if (view.current === name && view.media === el.video) togglePlayback();
  }, 300);
});
el.video.addEventListener("dblclick", () => {
  clearTimeout(videoClickTimer);
  toggleFullscreen();
});
el.seek.addEventListener("input", () => {
  view.seeking = true;
  el.elapsed.textContent = formatTime(Number(el.seek.value));
});
el.seek.addEventListener("change", () => {
  const media = view.media;
  view.seeking = false;
  playAt(Number(el.seek.value), !media.paused);
});
el.volume.addEventListener("input", () => {
  for (const media of [el.video, el.audio]) media.volume = Number(el.volume.value);
  saveLocalState(true);
});
el.fullscreen.addEventListener("click", toggleFullscreen);
window.addEventListener("pagehide", () => {
  view.leaving = true;
  saveLocalState(true);
  saveProgress(true);
});
window.addEventListener("pageshow", () => { view.leaving = false; });
document.addEventListener("visibilitychange", () => {
  if (document.hidden) {
    saveLocalState(true);
    saveProgress(true);
  }
});

async function initialize() {
  const saved = readLocalState();
  if (saved && typeof saved === "object") {
    if (Number.isFinite(saved.volume) && saved.volume >= 0 && saved.volume <= 1) el.volume.value = saved.volume;
    if (typeof saved.search === "string") el.search.value = saved.search;
    if ([...el.sort.options].some((option) => option.value === saved.sort)) el.sort.value = saved.sort;
    if (["all", "watched", "unwatched"].includes(saved.filter)) {
      view.filter = saved.filter;
      document.querySelectorAll(".filter").forEach((button) => {
        button.classList.toggle("active", button.dataset.filter === view.filter);
        button.setAttribute("aria-pressed", String(button.dataset.filter === view.filter));
      });
    }
  }
  await refresh();
  if (saved && typeof saved.name === "string" && view.recordings.some((file) => file.name === saved.name)) {
    const position = Number.isFinite(saved.position) && saved.position >= 0 ? saved.position : undefined;
    await openRecording(saved.name, { start: position, shouldPlay: saved.playing === true });
  }
}

initialize();
setInterval(refresh, 60_000);
