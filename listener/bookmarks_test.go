package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func bookmarkTestApp(t *testing.T) (*app, http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	name := "testdj-2026-11-16.ts"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("test recording"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	store, err := loadWatched(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &app{dir: dir, store: store, meta: map[string]cachedInfo{name: {size: file.Size(), modified: file.ModTime(), info: mediaInfo{Kind: "audio", Duration: 120}}}}
	handler, err := a.routes()
	if err != nil {
		t.Fatal(err)
	}
	return a, handler, name
}

func TestBookmarksPersistAcrossPlaybackAndWatchedUpdates(t *testing.T) {
	a, handler, name := bookmarkTestApp(t)
	path := "/api/recordings/" + name + "/bookmarks"
	for _, body := range []string{`{"seconds":60,"label":"Great song 🎵"}`, `{"seconds":5}`, `{"seconds":60.1,"label":"Repeated tap"}`} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("add bookmark: %d %s", response.Code, response.Body.String())
		}
	}
	items := a.store.bookmarks(name)
	if len(items) != 2 || items[0].Seconds != 5 || items[1].Label != "Great song 🎵" || len(items[1].ID) != 32 {
		t.Fatalf("unexpected bookmarks: %+v", items)
	}
	if err := a.store.setProgress(name, 30); err != nil {
		t.Fatal(err)
	}
	if err := a.store.setFavorite(name, true); err != nil {
		t.Fatal(err)
	}
	if err := a.store.set([]string{name}, true); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadWatched(a.store.path)
	if err != nil {
		t.Fatal(err)
	}
	a.store = reloaded
	if len(a.store.bookmarks(name)) != 2 || !a.store.isFavorite(name) || !a.store.isWatched(name) {
		t.Fatal("bookmarks, favorites, or watched status did not survive reload")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/recordings", nil))
	var library struct {
		Recordings []recording `json:"recordings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &library); err != nil || len(library.Recordings) != 1 || len(library.Recordings[0].Bookmarks) != 2 {
		t.Fatalf("bookmarks missing from library: %s (%v)", response.Body.String(), err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, path+"/"+items[1].ID, nil))
	if response.Code != http.StatusOK || len(a.store.bookmarks(name)) != 1 {
		t.Fatalf("remove bookmark: %d %s", response.Code, response.Body.String())
	}
	reloaded, err = loadWatched(a.store.path)
	if err != nil || len(reloaded.bookmarks(name)) != 1 {
		t.Fatal("bookmark removal did not persist", err)
	}
}

func TestBookmarkValidationAndMissingRecording(t *testing.T) {
	a, handler, name := bookmarkTestApp(t)
	path := "/api/recordings/" + name + "/bookmarks"
	for _, body := range []string{`{}`, `{"seconds":-1}`, `{"seconds":120}`, `{"seconds":1e999}`, `{"seconds":2,"unknown":true}`, `{"seconds":null}`, `{"seconds":2}{"seconds":3}`, `{"seconds":2} trailing`, `{"seconds":2,"label":"` + strings.Repeat("a", 121) + `"}`} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status %d", body, response.Code)
		}
	}
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/recordings/missing.ts/bookmarks", strings.NewReader(`{"seconds":2}`)),
		httptest.NewRequest(http.MethodDelete, path+"/unknown", nil),
	} {
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("missing target: status %d", response.Code)
		}
	}
	if len(a.store.bookmarks(name)) != 0 {
		t.Fatal("invalid requests changed bookmarks")
	}
}

func TestBookmarksRejectCrossOriginAndNonJSONWrites(t *testing.T) {
	const addr = "192.168.1.103:8787"
	for _, test := range []struct {
		origin, contentType string
		want                int
	}{
		{"http://foreign.example", "text/plain", http.StatusForbidden},
		{"http://foreign.example", "application/json", http.StatusForbidden},
		{"null", "application/json", http.StatusForbidden},
		{"http://" + addr, "text/plain", http.StatusUnsupportedMediaType},
		{"", "", http.StatusUnsupportedMediaType},
		{"http://" + addr, "application/json; charset=utf-8", http.StatusOK},
		{"", "application/json", http.StatusOK},
	} {
		a, handler, name := bookmarkTestApp(t)
		request := httptest.NewRequest(http.MethodPost, "http://"+addr+"/api/recordings/"+name+"/bookmarks", strings.NewReader(`{"seconds":20}`))
		request.RemoteAddr = "192.168.1.25:50000"
		request.Header.Set("Origin", test.origin)
		request.Header.Set("Content-Type", test.contentType)
		response := httptest.NewRecorder()
		localHostOnly(addr, handler).ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("origin %q, type %q: status %d, want %d", test.origin, test.contentType, response.Code, test.want)
		}
		if test.want != http.StatusOK && len(a.store.bookmarks(name)) != 0 {
			t.Fatal("rejected request changed saved bookmarks")
		}
	}
}

type bookmarkReadSignal struct {
	io.ReadCloser
	started chan struct{}
	once    sync.Once
}

func (b *bookmarkReadSignal) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	return b.ReadCloser.Read(p)
}

func TestSlowBookmarkUploadDoesNotBlockOtherOperationsAndTimesOut(t *testing.T) {
	for _, body := range []string{`{"seconds":`, `{"seconds":20}`} {
		t.Run(body, func(t *testing.T) {
			_, handler, name := bookmarkTestApp(t)
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					r.Body = &bookmarkReadSignal{ReadCloser: r.Body, started: started}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			path := "/api/recordings/" + name + "/bookmarks"
			if _, err := fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 64\r\n\r\n%s", path, server.Listener.Addr(), body); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("POST did not start reading its body")
			}
			client := &http.Client{Timeout: time.Second}
			response, err := client.Get(server.URL + path)
			if err != nil {
				t.Fatal("GET was blocked by the unfinished POST:", err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatal("GET status:", response.StatusCode)
			}
			request, err := http.NewRequest(http.MethodPatch, server.URL+"/api/favorites", strings.NewReader(fmt.Sprintf(`{"name":%q,"favorite":true}`, name)))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			response, err = client.Do(request)
			if err != nil {
				t.Fatal("favorite update was blocked by the unfinished POST:", err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatal("favorite update status:", response.StatusCode)
			}
			conn.SetReadDeadline(time.Now().Add(7 * time.Second))
			timedOut, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal("unfinished POST was not rejected within its read deadline:", err)
			}
			timedOut.Body.Close()
			if timedOut.StatusCode != http.StatusBadRequest {
				t.Fatal("unfinished POST status:", timedOut.StatusCode)
			}
		})
	}
}

func TestBookmarkSaveFailureKeepsStateAndDeleteCleansBookmarks(t *testing.T) {
	a, _, name := bookmarkTestApp(t)
	item := bookmark{ID: "test-id", Seconds: 10, Label: "A song"}
	if err := a.store.changeBookmark(name, item, false); err != nil {
		t.Fatal(err)
	}
	path := a.store.path
	a.store.path = filepath.Join(t.TempDir(), "missing", "state.json")
	if err := a.store.changeBookmark(name, item, true); err == nil || len(a.store.bookmarks(name)) != 1 {
		t.Fatal("failed save changed in-memory bookmarks")
	}
	a.store.path = path
	if err := a.store.remove(name); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadWatched(path)
	if err != nil || len(reloaded.bookmarks(name)) != 0 {
		t.Fatal("deleting an otherwise unwatched recording left bookmarks", err)
	}
}
