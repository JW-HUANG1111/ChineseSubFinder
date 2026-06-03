package zimuku

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ChineseSubFinder/ChineseSubFinder/pkg/cache_center"
	"github.com/ChineseSubFinder/ChineseSubFinder/pkg/logic/file_downloader"
	"github.com/ChineseSubFinder/ChineseSubFinder/pkg/settings"
	"github.com/sirupsen/logrus"
)

var (
	initOnce sync.Once
)

// initSettings initializes the global settings singleton with a temp dir.
// Many methods under test read settings.Get() so it must be primed once.
func initSettings(t *testing.T) {
	t.Helper()
	initOnce.Do(func() {
		dir := t.TempDir()
		settings.SetConfigRootPath(dir)
		_ = settings.Get() // trigger initialisation + writes a default config.yaml
	})
}

func newTestSupplier(t *testing.T) *Supplier {
	t.Helper()
	initSettings(t)
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)
	cache := cache_center.NewCacheCenter("test-zimuku", log)
	fd := file_downloader.NewFileDownloader(cache)
	return NewSupplier(fd)
}

// --- constructor & simple interface ---

func TestNewSupplier(t *testing.T) {
	s := newTestSupplier(t)
	if s == nil {
		t.Fatal("NewSupplier returned nil")
	}
	if s.log == nil {
		t.Error("logger not set")
	}
	if !s.isAlive {
		t.Error("isAlive default should be true")
	}
}

func TestGetSupplierName(t *testing.T) {
	s := newTestSupplier(t)
	if got := s.GetSupplierName(); got != "zimuku" {
		t.Errorf("GetSupplierName() = %q, want %q", got, "zimuku")
	}
}

func TestIsAlive_DefaultTrue(t *testing.T) {
	s := newTestSupplier(t)
	if !s.IsAlive() {
		t.Error("IsAlive() = false, want true (default)")
	}
}

func TestIsAlive_Flip(t *testing.T) {
	s := newTestSupplier(t)
	s.isAlive = false
	if s.IsAlive() {
		t.Error("IsAlive() = true after flip")
	}
}

func TestGetLogger(t *testing.T) {
	s := newTestSupplier(t)
	if s.GetLogger() == nil {
		t.Error("GetLogger() = nil")
	}
}

func TestOverDailyDownloadLimit(t *testing.T) {
	s := newTestSupplier(t)
	// Default NewOneSupplierSettings uses DailyDownloadLimit=20
	// So OverDailyDownloadLimit should return false (no limit hit)
	if s.OverDailyDownloadLimit() {
		t.Error("OverDailyDownloadLimit() = true, want false (default limit=20)")
	}
}

// --- pure helpers ---

func TestGetExt(t *testing.T) {
	cases := []struct {
		href, want string
	}{
		{"https://example.com/sub.zip", ".zip"},
		{"https://example.com/sub.ZIP", ".zip"},
		{"https://example.com/sub.srt", ".srt"},
		{"https://example.com/sub.ass", ".ass"},
		{"https://example.com/sub.ssa", ".ssa"},
		{"https://example.com/noext", ".zip"},
	}
	for _, tc := range cases {
		if got := getExt(tc.href); got != tc.want {
			t.Errorf("getExt(%q) = %q, want %q", tc.href, got, tc.want)
		}
	}
}

func TestMakeAbsoluteUrl(t *testing.T) {
	s := newTestSupplier(t)
	root := "https://zimuku.org"
	cases := []struct {
		href, want string
	}{
		{"https://cdn.example.com/a.zip", "https://cdn.example.com/a.zip"},
		{"/sub/123", "https://zimuku.org/sub/123"},
		{"sub/123", "https://zimuku.org/sub/123"},
		{"", "https://zimuku.org/"},
	}
	for _, tc := range cases {
		if got := s.makeAbsoluteUrl(tc.href, root); got != tc.want {
			t.Errorf("makeAbsoluteUrl(%q, %q) = %q, want %q", tc.href, root, got, tc.want)
		}
	}
}

// --- HTML parsing ---

func TestParseSearchResult_Empty(t *testing.T) {
	s := newTestSupplier(t)
	items, err := s.parseSearchResult("", true)
	if err != nil {
		t.Fatalf("parseSearchResult empty input error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items from empty HTML, got %d", len(items))
	}
}

func TestParseSearchResult_ValidHTML(t *testing.T) {
	s := newTestSupplier(t)
	html := `<html><body>
<div class="item">
  <a href="/sub/abc123/">Some.Movie.2020.1080p.BluRay.x264</a>
</div>
<div class="item">
  <a href="/sub/def456/">My.Show.S01E01.720p.WEB-DL</a>
</div>
<div class="item">
  <a href="/sub/ghi789/">Another.Show.S01.1080p</a>
</div>
<div class="item">
  <a href="/sub/xyz000/"></a>
</div>
</body></html>`
	items, err := s.parseSearchResult(html, true)
	if err != nil {
		t.Fatalf("parseSearchResult error: %v", err)
	}
	// 3 valid items; the 4th has empty title and is skipped
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if items[0].Title != "Some.Movie.2020.1080p.BluRay.x264" {
		t.Errorf("items[0].Title = %q", items[0].Title)
	}
	if !strings.HasPrefix(items[0].RUrl, "https://zimuku.org/") {
		t.Errorf("items[0].RUrl = %q, want https://zimuku.org/...", items[0].RUrl)
	}
	if !items[0].IsMovie {
		t.Error("items[0].IsMovie should be true (passed isMovie=true)")
	}
	if !items[2].IsFullSeason {
		t.Errorf("items[2] (S01 title) IsFullSeason = %v, want true", items[2].IsFullSeason)
	}
}

func TestParseSubPage_Empty(t *testing.T) {
	s := newTestSupplier(t)
	got := s.parseSubPage("", "/tmp/movie.mkv", 1, 1)
	if len(got) != 0 {
		t.Errorf("expected 0 subs from empty HTML, got %d", len(got))
	}
}

func TestParseSubPage_ValidHTML(t *testing.T) {
	s := newTestSupplier(t)
	html := `<html><body>
<a href="https://cdn.example.com/a.zip">下载字幕</a>
<a href="/local/file/abc.srt">.srt link</a>
<a href="another.ass">relative ass</a>
</body></html>`
	got := s.parseSubPage(html, "/tmp/movie.mkv", 2, 5)
	if len(got) != 3 {
		t.Fatalf("expected 3 subs, got %d: %+v", len(got), got)
	}
	// Field assertions
	if got[0].FromWhere != "zimuku" {
		t.Errorf("got[0].FromWhere = %q, want zimuku", got[0].FromWhere)
	}
	if got[0].FileUrl != "https://cdn.example.com/a.zip" {
		t.Errorf("got[0].FileUrl = %q", got[0].FileUrl)
	}
	if got[0].Ext != ".zip" {
		t.Errorf("got[0].Ext = %q, want .zip", got[0].Ext)
	}
	if got[0].Season != 2 || got[0].Episode != 5 {
		t.Errorf("got[0] = {S:%d E:%d}, want S:2 E:5", got[0].Season, got[0].Episode)
	}
	// Relative path is resolved against Zimuku.RootUrl
	if !strings.HasPrefix(got[1].FileUrl, "https://zimuku.org/") {
		t.Errorf("got[1].FileUrl = %q, want https://zimuku.org/...", got[1].FileUrl)
	}
	if got[2].Ext != ".ass" {
		t.Errorf("got[2].Ext = %q, want .ass", got[2].Ext)
	}
}

// --- network error paths (httptest) ---

func TestCheckAlive_HTTPDown(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer ts.Close()

	// Patch settings.Zimuku.RootUrl to point at the test server so the
	// "GET /search?q=" request lands there.
	settings.Get().AdvancedSettings.SuppliersSettings.Zimuku.RootUrl = ts.URL
	settings.Get().AdvancedSettings.SuppliersSettings.Zimuku.SearchUrl = "/?q=%s"

	s := newTestSupplier(t)
	alive, _ := s.CheckAlive()
	if alive {
		t.Error("CheckAlive() = true, want false (server returned 500)")
	}
}

func TestCheckAlive_OK(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>ok</html>"))
	}))
	defer ts.Close()

	settings.Get().AdvancedSettings.SuppliersSettings.Zimuku.RootUrl = ts.URL
	settings.Get().AdvancedSettings.SuppliersSettings.Zimuku.SearchUrl = "/?q=%s"

	s := newTestSupplier(t)
	alive, _ := s.CheckAlive()
	if !alive {
		t.Error("CheckAlive() = false, want true")
	}
}

func TestSearchKeyword_HTTPError(t *testing.T) {
	// resty treats 5xx as a non-error response (no network failure), so we
	// instead test the *connection refused* path by closing the server
	// immediately. That guarantees httpClient.R().Get returns an err.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := ts.URL
	ts.Close()

	settings.Get().AdvancedSettings.SuppliersSettings.Zimuku.RootUrl = url
	settings.Get().AdvancedSettings.SuppliersSettings.Zimuku.SearchUrl = "/?q=%s"

	s := newTestSupplier(t)
	_, err := s.searchKeyword("test", true)
	if err == nil {
		t.Fatal("searchKeyword() expected error from unreachable server, got nil")
	}
	if !strings.Contains(err.Error(), "http get") {
		t.Errorf("error = %v, want it to mention 'http get'", err)
	}
}

func TestSearchKeyword_5xxResponse_NoItems(t *testing.T) {
	// resty returns no error on 5xx; parseSearchResult then sees the body.
	// An empty 500 body should produce 0 search results.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		// empty body
	}))
	defer ts.Close()

	settings.Get().AdvancedSettings.SuppliersSettings.Zimuku.RootUrl = ts.URL
	settings.Get().AdvancedSettings.SuppliersSettings.Zimuku.SearchUrl = "/?q=%s"

	s := newTestSupplier(t)
	items, err := s.searchKeyword("test", true)
	if err != nil {
		t.Fatalf("searchKeyword() unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items from 500 empty body, got %d", len(items))
	}
}

func TestSearchKeyword_ValidHTML(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body>
<div class="item">
  <a href="/sub/zzz999/">A.Movie.2020.1080p</a>
</div>
</body></html>`))
	}))
	defer ts.Close()

	settings.Get().AdvancedSettings.SuppliersSettings.Zimuku.RootUrl = ts.URL
	settings.Get().AdvancedSettings.SuppliersSettings.Zimuku.SearchUrl = "/?q=%s"

	s := newTestSupplier(t)
	items, err := s.searchKeyword("A Movie", true)
	if err != nil {
		t.Fatalf("searchKeyword() error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Title != "A.Movie.2020.1080p" {
		t.Errorf("items[0].Title = %q", items[0].Title)
	}
}
