const el = Object.fromEntries([
  "folder", "total-count", "unwatched-count", "watched-count", "refresh",
  "search", "sort", "select-visible", "selection-count", "mark-selected",
  "unmark-selected", "mark-all", "list", "list-empty", "player-kind",
  "artwork-icon", "video", "audio", "player-title", "player-subtitle",
  "seek", "elapsed", "duration", "back", "play", "forward", "volume",
  "speed", "fullscreen", "player-watched", "notice"
].map((id) => [id, document.getElementById(id)]));

const view = {
  recordings: [], selected: new Set(), filter: "all", current: null,
  info: null, media: null, start: 0, seeking: false, loading: false
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

function displayName(name) {
  return name.replace(/\.ts$/i, "").replace(/-(\d{4}-\d{2}-\d{2}|\d{2}-\d{2}-\d{4})(-\d+)?$/, "");
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
    const row = document.createElement("div");
    row.className = "recording-row" + (view.current === file.name ? " active" : "");
    row.setAttribute("role", "button");
    row.setAttribute("tabindex", "0");
    row.setAttribute("aria-label", `Play ${file.name}`);
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
    icon.textContent = "▶";
    icon.setAttribute("aria-hidden", "true");
    const main = document.createElement("div");
    main.className = "row-main";
    const title = document.createElement("div");
    title.className = "row-title";
    title.textContent = file.name;
    title.title = file.name;
    const meta = document.createElement("div");
    meta.className = "row-meta";
    for (const value of [displayName(file.name), formatSize(file.size), new Date(file.modified).toLocaleDateString()]) {
      const span = document.createElement("span");
      span.textContent = value;
      meta.append(span);
    }
    main.append(title, meta);
    const badge = document.createElement("span");
    badge.className = "watch-badge" + (file.watched ? " watched" : "");
    badge.textContent = file.watched ? "WATCHED" : "NEW";
    row.append(checkbox, icon, main, badge);
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

function stopMedia() {
  for (const media of [el.video, el.audio]) {
    media.pause();
    media.removeAttribute("src");
    media.load();
    media.hidden = true;
  }
  view.media = null;
  el["artwork-icon"].hidden = false;
}

function streamURL(start) {
  return `/api/recordings/${encodeURIComponent(view.current)}/stream?start=${start.toFixed(3)}`;
}

async function playAt(start, shouldPlay = true) {
  if (!view.current || !view.info) return;
  const media = view.media;
  view.start = Math.max(0, Math.min(start, Math.max(0, view.info.duration - 0.5)));
  view.loading = true;
  media.src = streamURL(view.start);
  media.volume = Number(el.volume.value);
  media.playbackRate = Number(el.speed.value);
  media.load();
  updateTimeline();
  if (shouldPlay) {
    try {
      await media.play();
    } catch (error) {
      if (error.name !== "AbortError") notify(`Playback could not start: ${error.message}`);
    }
  }
  view.loading = false;
}

async function openRecording(name) {
  if (view.current === name) {
    if (view.media?.paused) view.media.play().catch((error) => notify(error.message));
    return;
  }
  stopMedia();
  view.current = name;
  view.info = null;
  view.start = 0;
  notify("Inspecting recording…");
  el["player-title"].textContent = name;
  el["player-subtitle"].textContent = "Loading media details";
  el["player-kind"].textContent = "LOADING";
  renderList();
  try {
    const info = await api(`/api/recordings/${encodeURIComponent(name)}/info`);
    if (view.current !== name) return;
    view.info = info;
    view.media = info.kind === "video" ? el.video : el.audio;
    view.media.hidden = info.kind !== "video";
    el["artwork-icon"].hidden = info.kind === "video";
    el["player-kind"].textContent = info.kind.toUpperCase();
    el["player-subtitle"].textContent = `${displayName(name)} · ${formatTime(info.duration)}`;
    el.duration.textContent = formatTime(info.duration);
    el.seek.max = String(Math.floor(info.duration));
    for (const id of ["seek", "back", "play", "forward", "player-watched"]) el[id].disabled = false;
    el.fullscreen.hidden = info.kind !== "video";
    updateWatchedButton();
    notify("");
    await playAt(0);
  } catch (error) {
    if (view.current === name) {
      el["player-kind"].textContent = "ERROR";
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
  el["player-watched"].textContent = file?.watched ? "Mark as unwatched" : "Mark as watched";
}

for (const media of [el.video, el.audio]) {
  media.addEventListener("timeupdate", () => { if (media === view.media) updateTimeline(); });
  media.addEventListener("playing", () => {
    if (media === view.media) {
      el.play.textContent = "Ⅱ";
      el.play.setAttribute("aria-label", "Pause");
      notify("");
    }
  });
  media.addEventListener("pause", () => {
    if (media === view.media) {
      el.play.textContent = "▶";
      el.play.setAttribute("aria-label", "Play");
    }
  });
  media.addEventListener("ended", () => {
    if (media === view.media) {
      updateTimeline();
      const file = view.recordings.find((item) => item.name === view.current);
      if (file && !file.watched && view.start + media.currentTime >= view.info.duration - 10) {
        setWatched([file.name], true);
      }
    }
  });
  media.addEventListener("error", () => {
    if (media === view.media && media.error && !view.loading) {
      notify("Playback failed. Check the listener log and FFmpeg installation.");
    }
  });
}

el.refresh.addEventListener("click", refresh);
el.search.addEventListener("input", renderList);
el.sort.addEventListener("change", renderList);
document.querySelectorAll(".filter").forEach((button) => {
  button.addEventListener("click", () => {
    view.filter = button.dataset.filter;
    document.querySelectorAll(".filter").forEach((other) => {
      other.classList.toggle("active", other === button);
      other.setAttribute("aria-pressed", other === button ? "true" : "false");
    });
    renderList();
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
el.play.addEventListener("click", () => {
  const media = view.media;
  if (!media) return;
  if (media.paused) media.play().catch((error) => notify(error.message));
  else media.pause();
});
el.back.addEventListener("click", () => {
  const media = view.media;
  playAt(view.start + media.currentTime - 15, !media.paused);
});
el.forward.addEventListener("click", () => {
  const media = view.media;
  playAt(view.start + media.currentTime + 15, !media.paused);
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
});
el.speed.addEventListener("change", () => {
  for (const media of [el.video, el.audio]) media.playbackRate = Number(el.speed.value);
});
el.fullscreen.addEventListener("click", () => el.video.requestFullscreen());

refresh();
setInterval(refresh, 60_000);
