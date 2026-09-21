package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var webFiles embed.FS

type config struct {
	MediaDir   string `json:"media_dir"`
	ListenAddr string `json:"listen_addr"`
}

type recording struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Watched  bool      `json:"watched"`
	Progress float64   `json:"progress,omitempty"`
}

type mediaInfo struct {
	Kind     string  `json:"kind"`
	Duration float64 `json:"duration"`
}

type cachedInfo struct {
	size     int64
	modified time.Time
	info     mediaInfo
}

type watchedFile struct {
	Watched  map[string]bool    `json:"watched"`
	Progress map[string]float64 `json:"progress,omitempty"`
}

type watchedStore struct {
	mu   sync.Mutex
	path string
	data watchedFile
}

type app struct {
	dir     string
	ffmpeg  string
	ffprobe string
	store   *watchedStore
	metaMu  sync.Mutex
	meta    map[string]cachedInfo
}

func main() {
	configPath := flag.String("config", "config.json", "path to listener config")
	flag.Parse()
	absoluteConfig, err := filepath.Abs(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(filepath.Dir(absoluteConfig), "listener.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		log.Fatal(err)
	}
	log.SetOutput(logFile)
	if err := run(absoluteConfig); err != nil {
		log.Print(err)
		logFile.Close()
		os.Exit(1)
	}
	logFile.Close()
}

func run(configPath string) error {
	rawConfig, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", configPath, err)
	}
	var cfg config
	if err := json.Unmarshal(rawConfig, &cfg); err != nil {
		return fmt.Errorf("parse listener config: %w", err)
	}
	if cfg.MediaDir == "" || cfg.ListenAddr == "" {
		return errors.New("media_dir and listen_addr are required")
	}
	host, _, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("listen_addr must use a loopback IP, for example 127.0.0.1:8787")
	}
	mediaDir, err := filepath.Abs(cfg.MediaDir)
	if err != nil {
		return err
	}
	dirInfo, err := os.Stat(mediaDir)
	if err != nil || !dirInfo.IsDir() {
		return fmt.Errorf("media folder is unavailable: %s", mediaDir)
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return errors.New("FFmpeg is required on PATH")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		return errors.New("FFprobe is required on PATH")
	}
	store, err := loadWatched(filepath.Join(filepath.Dir(configPath), "state.json"))
	if err != nil {
		return err
	}
	serverApp := &app{dir: mediaDir, ffmpeg: ffmpeg, ffprobe: ffprobe, store: store, meta: make(map[string]cachedInfo)}
	mux, err := serverApp.routes()
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           localHostOnly(cfg.ListenAddr, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("Listening at http://%s; media folder %s", cfg.ListenAddr, mediaDir)
	return server.ListenAndServe()
}

func localHostOnly(addr string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != addr {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) routes() (http.Handler, error) {
	staticFiles, err := fs.Sub(webFiles, "web")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(staticFiles)))
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/recordings", a.listHandler)
	mux.HandleFunc("PATCH /api/watched", a.watchedHandler)
	mux.HandleFunc("PATCH /api/progress", a.progressHandler)
	mux.HandleFunc("GET /api/recordings/{name}/info", a.infoHandler)
	mux.HandleFunc("GET /api/recordings/{name}/stream", a.streamHandler)
	return mux, nil
}

func (a *app) files() ([]recording, error) {
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		return nil, err
	}
	files := make([]recording, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !validName(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		watched, progress := a.store.status(name)
		files = append(files, recording{Name: name, Size: info.Size(), Modified: info.ModTime(), Watched: watched, Progress: progress})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Modified.Equal(files[j].Modified) {
			return files[i].Name < files[j].Name
		}
		return files[i].Modified.After(files[j].Modified)
	})
	return files, nil
}

func validName(name string) bool {
	if !strings.EqualFold(filepath.Ext(name), ".ts") || len(name) < 4 {
		return false
	}
	for _, char := range strings.TrimSuffix(name, filepath.Ext(name)) {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func (a *app) file(name string) (string, os.FileInfo, error) {
	if !validName(name) {
		return "", nil, os.ErrNotExist
	}
	path := filepath.Join(a.dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, os.ErrNotExist
	}
	return path, info, nil
}

func (a *app) listHandler(w http.ResponseWriter, r *http.Request) {
	files, err := a.files()
	if err != nil {
		http.Error(w, "cannot read media folder", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"folder": a.dir, "recordings": files})
}

func (a *app) watchedHandler(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Names   []string `json:"names"`
		All     bool     `json:"all"`
		Watched bool     `json:"watched"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	var names []string
	if request.All {
		files, err := a.files()
		if err != nil {
			http.Error(w, "cannot read media folder", http.StatusInternalServerError)
			return
		}
		for _, file := range files {
			names = append(names, file.Name)
		}
	} else {
		if len(request.Names) == 0 {
			http.Error(w, "select at least one recording", http.StatusBadRequest)
			return
		}
		for _, name := range request.Names {
			if _, _, err := a.file(name); err != nil {
				http.Error(w, "recording not found", http.StatusBadRequest)
				return
			}
		}
		names = request.Names
	}
	if err := a.store.set(names, request.Watched); err != nil {
		http.Error(w, "cannot save watched status", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]int{"updated": len(names)})
}

func (a *app) progressHandler(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name    string  `json:"name"`
		Seconds float64 `json:"seconds"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || math.IsNaN(request.Seconds) || math.IsInf(request.Seconds, 0) || request.Seconds < 0 {
		http.Error(w, "invalid progress", http.StatusBadRequest)
		return
	}
	if _, _, err := a.file(request.Name); err != nil {
		http.Error(w, "recording not found", http.StatusBadRequest)
		return
	}
	if err := a.store.setProgress(request.Name, request.Seconds); err != nil {
		http.Error(w, "cannot save progress", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"saved": true})
}

func (a *app) infoHandler(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path, file, err := a.file(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	info, err := a.probe(r.Context(), name, path, file)
	if err != nil {
		log.Printf("Probe failed for %s: %v", name, err)
		http.Error(w, "cannot inspect recording", http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, info)
}

func (a *app) probe(ctx context.Context, name, path string, file os.FileInfo) (mediaInfo, error) {
	a.metaMu.Lock()
	cached, ok := a.meta[name]
	a.metaMu.Unlock()
	if ok && cached.size == file.Size() && cached.modified.Equal(file.ModTime()) {
		return cached.info, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(probeCtx, a.ffprobe, "-v", "error", "-show_entries",
		"format=duration:stream=codec_type", "-of", "json", path)
	hideCommandWindow(command)
	output, err := command.Output()
	if err != nil {
		return mediaInfo{}, err
	}
	var result struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return mediaInfo{}, err
	}
	duration, err := strconv.ParseFloat(result.Format.Duration, 64)
	if err != nil || duration <= 0 {
		return mediaInfo{}, errors.New("recording duration is unavailable")
	}
	info := mediaInfo{Duration: duration}
	for _, stream := range result.Streams {
		switch stream.CodecType {
		case "video":
			info.Kind = "video"
		case "audio":
			if info.Kind == "" {
				info.Kind = "audio"
			}
		}
	}
	if info.Kind == "" {
		return mediaInfo{}, errors.New("recording has no audio or video stream")
	}
	a.metaMu.Lock()
	a.meta[name] = cachedInfo{size: file.Size(), modified: file.ModTime(), info: info}
	a.metaMu.Unlock()
	return info, nil
}

type flushWriter struct{ http.ResponseWriter }

func (w flushWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
	return n, err
}

func (a *app) streamHandler(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path, file, err := a.file(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	info, err := a.probe(r.Context(), name, path, file)
	if err != nil {
		http.Error(w, "cannot inspect recording", http.StatusUnprocessableEntity)
		return
	}
	start := 0.0
	if raw := r.URL.Query().Get("start"); raw != "" {
		start, err = strconv.ParseFloat(raw, 64)
		if err != nil || start < 0 || start >= info.Duration || start != start {
			http.Error(w, "invalid start time", http.StatusBadRequest)
			return
		}
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	if start > 0 {
		args = append(args, "-ss", strconv.FormatFloat(start, 'f', 3, 64))
	}
	args = append(args, "-i", path, "-map", "0:v:0?", "-map", "0:a:0?",
		"-dn", "-sn", "-c:v", "copy", "-c:a", "aac", "-b:a", "320k", "-avoid_negative_ts", "make_zero",
		"-movflags", "+frag_keyframe+empty_moov+default_base_moof",
		"-frag_duration", "2000000", "-f", "mp4", "pipe:1")
	command := exec.CommandContext(r.Context(), a.ffmpeg, args...)
	hideCommandWindow(command)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if info.Kind == "audio" {
		w.Header().Set("Content-Type", "audio/mp4")
	} else {
		w.Header().Set("Content-Type", "video/mp4")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	command.Stdout = flushWriter{w}
	if err := command.Run(); err != nil && r.Context().Err() == nil {
		log.Printf("Stream failed for %s: %v: %s", name, err, strings.TrimSpace(stderr.String()))
	}
}

func loadWatched(path string) (*watchedStore, error) {
	store := &watchedStore{path: path, data: watchedFile{Watched: make(map[string]bool), Progress: make(map[string]float64)}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &store.data); err != nil {
		return nil, fmt.Errorf("read watched state: %w", err)
	}
	if store.data.Watched == nil {
		store.data.Watched = make(map[string]bool)
	}
	if store.data.Progress == nil {
		store.data.Progress = make(map[string]float64)
	}
	return store, nil
}

func (s *watchedStore) isWatched(name string) bool {
	checked, _ := s.status(name)
	return checked
}

func (s *watchedStore) status(name string) (bool, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Watched[name], s.data.Progress[name]
}

func (s *watchedStore) set(names []string, watched bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]bool, len(s.data.Watched))
	for name, value := range s.data.Watched {
		next[name] = value
	}
	progress := make(map[string]float64, len(s.data.Progress))
	for name, value := range s.data.Progress {
		progress[name] = value
	}
	for _, name := range names {
		if watched {
			next[name] = true
			delete(progress, name)
		} else {
			delete(next, name)
		}
	}
	return s.save(watchedFile{Watched: next, Progress: progress})
}

func (s *watchedStore) setProgress(name string, seconds float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Watched[name] {
		return nil
	}
	progress := make(map[string]float64, len(s.data.Progress))
	for key, value := range s.data.Progress {
		progress[key] = value
	}
	if seconds == 0 {
		delete(progress, name)
	} else {
		progress[name] = seconds
	}
	return s.save(watchedFile{Watched: s.data.Watched, Progress: progress})
}

// save is called with the store mutex held.
func (s *watchedStore) save(next watchedFile) error {
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if err := json.NewEncoder(temp).Encode(next); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), s.path); err != nil {
		return err
	}
	s.data = next
	return nil
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("Write JSON response: %v", err)
	}
}
