package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type bookmark struct {
	ID      string  `json:"id"`
	Seconds float64 `json:"seconds"`
	Label   string  `json:"label"`
}

var errBookmarkNotFound = errors.New("bookmark not found")

func (a *app) bookmarkHandler(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path, file, err := a.file(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var item bookmark
	if r.Method == http.MethodPost {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			http.Error(w, "bookmark requests require application/json", http.StatusUnsupportedMediaType)
			return
		}
		controller := http.NewResponseController(w)
		// Bound slow uploads without limiting the long-lived audio responses.
		controller.SetReadDeadline(time.Now().Add(5 * time.Second))
		defer controller.SetReadDeadline(time.Time{})
		var request struct {
			Seconds *float64 `json:"seconds"`
			Label   string   `json:"label"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&request)
		var extra any
		if decodeErr == nil && decoder.Decode(&extra) != io.EOF {
			decodeErr = errors.New("expected one complete bookmark request")
		}
		if decodeErr != nil {
			// Do not drain an unfinished upload after clearing its read deadline.
			w.Header().Set("Connection", "close")
		}
		if decodeErr != nil || request.Seconds == nil ||
			math.IsNaN(*request.Seconds) || math.IsInf(*request.Seconds, 0) || *request.Seconds < 0 ||
			utf8.RuneCountInString(request.Label) > 120 {
			http.Error(w, "invalid bookmark", http.StatusBadRequest)
			return
		}
		controller.SetReadDeadline(time.Time{})
		info, err := a.probe(r.Context(), name, path, file)
		if err != nil {
			http.Error(w, "cannot inspect recording", http.StatusUnprocessableEntity)
			return
		}
		if *request.Seconds >= info.Duration {
			http.Error(w, "bookmark must be inside the recording", http.StatusBadRequest)
			return
		}
		label := strings.TrimSpace(request.Label)
		if label == "" {
			label = "Bookmark"
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			http.Error(w, "cannot create bookmark", http.StatusInternalServerError)
			return
		}
		item = bookmark{ID: hex.EncodeToString(id[:]), Seconds: *request.Seconds, Label: label}
	}
	if r.Method != http.MethodGet {
		a.recordingMu.Lock()
		defer a.recordingMu.Unlock()
		// Deletion may have completed while the request was being decoded or probed.
		if _, _, err := a.file(name); err != nil {
			http.NotFound(w, r)
			return
		}
		remove := r.Method == http.MethodDelete
		if remove {
			item.ID = r.PathValue("id")
		}
		err = a.store.changeBookmark(name, item, remove)
		if errors.Is(err, errBookmarkNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "cannot save bookmark", http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, map[string]any{"bookmarks": a.store.bookmarks(name)})
}

func (s *watchedStore) bookmarks(name string) []bookmark {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bookmark{}, s.data.Bookmarks[name]...)
}

func (s *watchedStore) changeBookmark(name string, item bookmark, remove bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := append([]bookmark{}, s.data.Bookmarks[name]...)
	if remove {
		found := false
		for i, existing := range items {
			if existing.ID == item.ID {
				items = append(items[:i], items[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			return errBookmarkNotFound
		}
	} else {
		for _, existing := range items {
			if math.Abs(existing.Seconds-item.Seconds) < 0.5 {
				return nil // Repeated taps at the same moment keep a single marker.
			}
		}
		if len(items) >= 500 {
			return errors.New("bookmark limit reached")
		}
		items = append(items, item)
		sort.Slice(items, func(i, j int) bool { return items[i].Seconds < items[j].Seconds })
	}
	bookmarks := make(map[string][]bookmark, len(s.data.Bookmarks)+1)
	for key, value := range s.data.Bookmarks {
		bookmarks[key] = value
	}
	if len(items) == 0 {
		delete(bookmarks, name)
	} else {
		bookmarks[name] = items
	}
	next := s.data
	next.Bookmarks = bookmarks
	return s.save(next)
}
