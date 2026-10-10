package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWatchedStatusPersistsAndBulkUpdate(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha-2026-09-21.ts", "beta-2026-09-21.ts"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("sample"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "unfinished.ts.part"), []byte("sample"), 0600); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "state.json")
	store, err := loadWatched(storePath)
	if err != nil {
		t.Fatal(err)
	}
	serverApp := &app{dir: dir, store: store}
	mux, err := serverApp.routes()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPatch, "/api/watched", strings.NewReader(`{"names":["alpha-2026-09-21.ts"],"watched":true}`))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("mark one watched: %d %s", response.Code, response.Body.String())
	}
	reloaded, err := loadWatched(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.isWatched("alpha-2026-09-21.ts") || reloaded.isWatched("beta-2026-09-21.ts") {
		t.Fatal("selected watched state did not persist")
	}
	serverApp.store = reloaded
	request = httptest.NewRequest(http.MethodPatch, "/api/watched", strings.NewReader(`{"all":true,"watched":true}`))
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("mark all watched: %d %s", response.Code, response.Body.String())
	}
	files, err := serverApp.files()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || !files[0].Watched || !files[1].Watched {
		t.Fatalf("completed files or bulk watched state incorrect: %+v", files)
	}
	request = httptest.NewRequest(http.MethodPatch, "/api/watched", strings.NewReader(`{"names":["../state.json"],"watched":true}`))
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("traversal name should be rejected, got %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/recordings", nil)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	var result struct {
		Recordings []recording `json:"recordings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Recordings) != 2 {
		t.Fatalf("expected only completed .ts files, got %d", len(result.Recordings))
	}
	if result.Recordings[0].HasChat || result.Recordings[1].HasChat {
		t.Fatal("recording without a chat sidecar reported chat")
	}
}

func TestProgressPersistsUntilWatched(t *testing.T) {
	dir := t.TempDir()
	name := "alpha-2026-09-21.ts"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("sample"), 0600); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "state.json")
	store, err := loadWatched(storePath)
	if err != nil {
		t.Fatal(err)
	}
	serverApp := &app{dir: dir, store: store}
	mux, err := serverApp.routes()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPatch, "/api/progress", strings.NewReader(`{"name":"alpha-2026-09-21.ts","seconds":65.5}`))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("save progress: %d %s", response.Code, response.Body.String())
	}
	reloaded, err := loadWatched(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, progress := reloaded.status(name); progress != 65.5 {
		t.Fatalf("progress after reload = %v, want 65.5", progress)
	}
	serverApp.store = reloaded
	request = httptest.NewRequest(http.MethodGet, "/api/recordings", nil)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	var listed struct {
		Recordings []recording `json:"recordings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Recordings) != 1 || listed.Recordings[0].Progress != 65.5 {
		t.Fatalf("listing omitted progress: %+v", listed.Recordings)
	}
	request = httptest.NewRequest(http.MethodPatch, "/api/watched", strings.NewReader(`{"names":["alpha-2026-09-21.ts"],"watched":true}`))
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("mark watched: %d %s", response.Code, response.Body.String())
	}
	reloaded, err = loadWatched(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if watched, progress := reloaded.status(name); !watched || progress != 0 {
		t.Fatalf("watched recording kept progress: watched=%v progress=%v", watched, progress)
	}
}

func TestFavoritesPersistAndProtectRecordingAndChat(t *testing.T) {
	dir := t.TempDir()
	name := "alpha-2026-09-21.ts"
	path := filepath.Join(dir, name)
	for _, file := range []string{path, chatPath(path)} {
		if err := os.WriteFile(file, []byte("sample"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	storePath := filepath.Join(t.TempDir(), "state.json")
	// Existing state files must keep watched status and progress when upgraded.
	if err := os.WriteFile(storePath, []byte(`{"watched":{"beta-2026-09-21.ts":true},"progress":{"alpha-2026-09-21.ts":65.5}}`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := loadWatched(storePath)
	if err != nil {
		t.Fatal(err)
	}
	serverApp := &app{dir: dir, store: store}
	mux, err := serverApp.routes()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPatch, "/api/favorites", strings.NewReader(`{"name":"alpha-2026-09-21.ts","favorite":true}`))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("favorite: %d %s", response.Code, response.Body.String())
	}
	if !store.isWatched("beta-2026-09-21.ts") {
		t.Fatal("favorite update lost existing watched status")
	}
	if _, progress := store.status(name); progress != 65.5 {
		t.Fatalf("favorite update lost progress: %v", progress)
	}
	if err := store.setProgress(name, 70); err != nil {
		t.Fatal(err)
	}
	if err := store.set([]string{name}, true); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadWatched(storePath)
	if err != nil {
		t.Fatal(err)
	}
	serverApp.store = reloaded
	files, err := serverApp.files()
	if err != nil || len(files) != 1 || !files[0].Favorite || !files[0].Watched {
		t.Fatalf("favorite did not survive progress, watched update, and reload: %+v (%v)", files, err)
	}
	request = httptest.NewRequest(http.MethodDelete, "/api/recordings/"+name, nil)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("favorite delete should be blocked: %d %s", response.Code, response.Body.String())
	}
	for _, file := range []string{path, chatPath(path)} {
		if data, err := os.ReadFile(file); err != nil || string(data) != "sample" {
			t.Fatalf("protected file changed: %s (%v)", file, err)
		}
	}
	if !reloaded.isFavorite(name) || !reloaded.isWatched(name) {
		t.Fatal("blocked deletion changed saved status")
	}
	request = httptest.NewRequest(http.MethodPatch, "/api/favorites", strings.NewReader(`{"name":"alpha-2026-09-21.ts","favorite":false}`))
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("remove favorite: %d %s", response.Code, response.Body.String())
	}
	reloaded, err = loadWatched(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.isFavorite(name) || !reloaded.isWatched(name) {
		t.Fatal("unfavorite did not persist or lost watched status")
	}
	request = httptest.NewRequest(http.MethodDelete, "/api/recordings/"+name, nil)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unstarred recording should allow deletion: %d %s", response.Code, response.Body.String())
	}
}

func TestFavoriteValidationAndSaveFailure(t *testing.T) {
	dir := t.TempDir()
	name := "alpha.ts"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("sample"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := loadWatched(filepath.Join(t.TempDir(), "missing", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	serverApp := &app{dir: dir, store: store}
	mux, err := serverApp.routes()
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"name":"../alpha.ts","favorite":true}`,
		`{"name":"missing.ts","favorite":true}`,
		`{"name":"alpha.ts"}`,
		`{"name":"alpha.ts","favorite":null}`,
		`{"name":"alpha.ts","favorite":"true"}`,
		`{"name":"alpha.ts","favorite":true,"other":1}`,
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/api/favorites", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid request %s: got %d", body, response.Code)
		}
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/api/favorites", strings.NewReader(`{"name":"alpha.ts","favorite":true}`)))
	if response.Code != http.StatusInternalServerError || store.isFavorite(name) {
		t.Fatalf("failed save must not claim protection: %d", response.Code)
	}
}

func TestDeleteRecordingAlsoDeletesChatSidecar(t *testing.T) {
	dir := t.TempDir()
	name := "alpha-2026-09-21.ts"
	chatName := "alpha-2026-09-21.chat.jsonl"
	for _, path := range []string{filepath.Join(dir, name), filepath.Join(dir, chatName)} {
		if err := os.WriteFile(path, []byte("sample"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := loadWatched(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	serverApp := &app{dir: dir, store: store}
	mux, err := serverApp.routes()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/recordings/"+name, nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("delete recording: %d %s", response.Code, response.Body.String())
	}
	for _, path := range []string{filepath.Join(dir, name), filepath.Join(dir, chatName)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("deleted file still exists: %s (%v)", path, err)
		}
	}
}

func TestChatSidecarIsListedAndServed(t *testing.T) {
	dir := t.TempDir()
	name := "alpha-2026-09-21.ts"
	chatName := "alpha-2026-09-21.chat.jsonl"
	chat := `{"offset_seconds":12.5,"username":"viewer","message":"hello"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("sample"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, chatName), []byte(chat), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := loadWatched(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	serverApp := &app{dir: dir, store: store}
	mux, err := serverApp.routes()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/recordings", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	var listed struct {
		Recordings []recording `json:"recordings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Recordings) != 1 || !listed.Recordings[0].HasChat {
		t.Fatalf("chat sidecar not listed: %+v", listed.Recordings)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/recordings/"+name+"/chat", nil)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != chat {
		t.Fatalf("chat response: %d %q", response.Code, response.Body.String())
	}
}

func TestProfileImageURL(t *testing.T) {
	const want = "https://static-cdn.jtvnw.net/jtv_user_pictures/example-profile_image-300x300.png"
	for _, test := range []struct {
		name string
		page string
		want string
	}{
		{name: "Twitch profile", page: `<meta property="og:image" content="` + want + `"/>`, want: want},
		{name: "Twitter image", page: `<meta name="twitter:image" content="` + want + `"/>`, want: want},
		{name: "attribute order", page: `<meta content="` + want + `" property="og:image">`, want: want},
		{name: "skip unrelated image", page: `<meta property="og:image" content="https://example.com/image.png"><meta name="twitter:image" content="` + want + `">`, want: want},
		{name: "unrelated image", page: `<meta property="og:image" content="https://example.com/image.png">`},
		{name: "missing image", page: `<title>Unavailable</title>`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := profileImageURL([]byte(test.page)); got != test.want {
				t.Fatalf("profileImageURL() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestServeAvatarImage(t *testing.T) {
	const image = "\x89PNG\r\n\x1a\nimage"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(image))
	}))
	defer server.Close()

	request := httptest.NewRequest(http.MethodGet, "/api/avatars/roice?image=1", nil)
	response := httptest.NewRecorder()
	serveAvatar(response, request, server.URL)
	if response.Code != http.StatusOK || response.Body.String() != image || response.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("image response: status=%d type=%q body=%q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}

func TestServeAvatarRejectsNonImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>not an image</html>"))
	}))
	defer server.Close()

	request := httptest.NewRequest(http.MethodGet, "/api/avatars/roice?image=1", nil)
	response := httptest.NewRecorder()
	serveAvatar(response, request, server.URL)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("non-image response: status=%d, want %d", response.Code, http.StatusBadGateway)
	}
}

func TestServeAvatarMissingImageIsNotCached(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/avatars/roice?image=1", nil)
	response := httptest.NewRecorder()
	serveAvatar(response, request, "")
	if response.Code != http.StatusNotFound || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing image response: status=%d cache=%q", response.Code, response.Header().Get("Cache-Control"))
	}
}
