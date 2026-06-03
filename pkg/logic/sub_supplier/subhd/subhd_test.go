package subhd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChineseSubFinder/ChineseSubFinder/pkg/cache_center"
	"github.com/ChineseSubFinder/ChineseSubFinder/pkg/logic/file_downloader"
	"github.com/ChineseSubFinder/ChineseSubFinder/pkg/settings"
	"github.com/sirupsen/logrus"
)

var (
	initOnceSubhd sync.Once
)

// initSettings primes the global settings singleton with a temp dir.
// Required by tests that exercise CheckAlive / searchKeyword (which read
// settings.Get().AdvancedSettings.SuppliersSettings.SubHD.*).
func initSettings(t *testing.T) {
	t.Helper()
	initOnceSubhd.Do(func() {
		dir := t.TempDir()
		settings.SetConfigRootPath(dir)
		_ = settings.Get() // triggers creation of default config.yaml
	})
}

func newTestSupplier(t *testing.T) *Supplier {
	t.Helper()
	initSettings(t)
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel) // keep test output clean
	cache := cache_center.NewCacheCenter("test-subhd", log)
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
	if s.httpClient == nil {
		t.Error("httpClient not set")
	}
	if s.httpClient.Timeout != 30*time.Second {
		t.Errorf("httpClient.Timeout = %v, want 30s", s.httpClient.Timeout)
	}
}

func TestGetSupplierName(t *testing.T) {
	s := newTestSupplier(t)
	if got := s.GetSupplierName(); got != "subhd" {
		t.Errorf("GetSupplierName() = %q, want %q", got, "subhd")
	}
}

func TestIsAlive_DefaultTrue(t *testing.T) {
	s := newTestSupplier(t)
	// NewSupplier sets isAlive = true
	if !s.IsAlive() {
		t.Error("IsAlive() = false, want true (default after NewSupplier)")
	}
}

func TestIsAlive_Flip(t *testing.T) {
	s := newTestSupplier(t)
	s.isAlive = false
	if s.IsAlive() {
		t.Error("IsAlive() = true after setting isAlive=false")
	}
	s.isAlive = true
	if !s.IsAlive() {
		t.Error("IsAlive() = false after setting isAlive=true")
	}
}

func TestGetLogger(t *testing.T) {
	s := newTestSupplier(t)
	if s.GetLogger() == nil {
		t.Error("GetLogger() returned nil")
	}
}

// --- pure helpers ---

func TestGetExt(t *testing.T) {
	cases := []struct {
		href string
		want string
	}{
		{"https://example.com/sub.zip", ".zip"},
		{"https://example.com/sub.ZIP", ".zip"},
		{"https://example.com/sub.srt", ".srt"},
		{"https://example.com/sub.ass", ".ass"},
		{"https://example.com/sub.ssa", ".ssa"},
		{"https://example.com/sub.tar.gz", ".zip"}, // unknown falls back to .zip
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
	root := "https://subhd.tv"
	cases := []struct {
		href, want string
	}{
		{"https://cdn.example.com/a.zip", "https://cdn.example.com/a.zip"}, // already absolute
		{"/a/abc", "https://subhd.tv/a/abc"},                              // root-relative
		{"a/abc", "https://subhd.tv/a/abc"},                               // path-relative
		{"", "https://subhd.tv/"},
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
	items, err := s.parseSearchResult(strings.NewReader(""), true)
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
<a class="link-dark align-middle" href="/a/abc123">The.Movie.2020.1080p.BluRay</a>
<a class="link-dark align-middle" href="/a/def456">My.Show.S01E01.720p</a>
<a class="link-dark align-middle" href="/a/ghi789">My.Show.S01.1080p</a>
<a class="link-dark align-middle" href="/a/xyz000"></a>
</body></html>`
	items, err := s.parseSearchResult(strings.NewReader(html), true)
	if err != nil {
		t.Fatalf("parseSearchResult error: %v", err)
	}
	// Last entry has empty title, should be skipped.
	if len(items) != 3 {
		t.Fatalf("expected 3 items (empty title filtered), got %d", len(items))
	}
	if items[0].Title != "The.Movie.2020.1080p.BluRay" {
		t.Errorf("items[0].Title = %q", items[0].Title)
	}
	if items[0].RUrl != "/a/abc123" {
		t.Errorf("items[0].RUrl = %q, want /a/abc123", items[0].RUrl)
	}
	if !items[0].IsMovie {
		t.Error("items[0].IsMovie should be true (passed isMovie=true)")
	}
	if items[2].IsFullSeason != true {
		t.Errorf("items[2] (S01 title) IsFullSeason = %v, want true", items[2].IsFullSeason)
	}
}

// --- FlareSolverr error paths ---

func TestCheckAlive_FlareSolverrDown(t *testing.T) {
	// Server returns 500 for any request
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer ts.Close()

	orig := FlareSolverrURL
	FlareSolverrURL = ts.URL
	defer func() { FlareSolverrURL = orig }()

	s := newTestSupplier(t)
	alive, _ := s.CheckAlive()
	if alive {
		t.Error("CheckAlive() = true, want false (FlareSolverr returned 500)")
	}
}

func TestCheckAlive_FlareSolverrUnreachable(t *testing.T) {
	// Point to a closed server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := ts.URL
	ts.Close() // close immediately so connection is refused

	orig := FlareSolverrURL
	FlareSolverrURL = url
	defer func() { FlareSolverrURL = orig }()

	s := newTestSupplier(t)
	alive, _ := s.CheckAlive()
	if alive {
		t.Error("CheckAlive() = true, want false (FlareSolverr unreachable)")
	}
}

func TestCheckAlive_FlareSolverrOK(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"solution": map[string]interface{}{
				"status":   200,
				"response": "<html></html>",
			},
		})
	}))
	defer ts.Close()

	orig := FlareSolverrURL
	FlareSolverrURL = ts.URL
	defer func() { FlareSolverrURL = orig }()

	s := newTestSupplier(t)
	alive, _ := s.CheckAlive()
	if !alive {
		t.Error("CheckAlive() = false, want true (FlareSolverr returned 200)")
	}
}

func TestSearchKeyword_FlareSolverrError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	orig := FlareSolverrURL
	FlareSolverrURL = ts.URL
	defer func() { FlareSolverrURL = orig }()

	s := newTestSupplier(t)
	_, err := s.searchKeyword("test", true)
	if err == nil {
		t.Error("searchKeyword() expected error from FlareSolverr 500, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "FlareSolverr") {
		t.Errorf("error = %v, want it to mention FlareSolverr", err)
	}
}

func TestSearchKeyword_EmptyResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"solution": map[string]interface{}{
				"status":   200,
				"response": "",
			},
		})
	}))
	defer ts.Close()

	orig := FlareSolverrURL
	FlareSolverrURL = ts.URL
	defer func() { FlareSolverrURL = orig }()

	s := newTestSupplier(t)
	items, err := s.searchKeyword("test", true)
	if err != nil {
		t.Fatalf("searchKeyword() unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items from empty HTML, got %d", len(items))
	}
}

func TestSearchKeyword_ValidHTML(t *testing.T) {
	htmlResp := `<html><body>
<a class="link-dark align-middle" href="/a/zzz999">Some.Movie.2020.1080p</a>
</body></html>`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"solution": map[string]interface{}{
				"status":   200,
				"response": htmlResp,
			},
		})
	}))
	defer ts.Close()

	orig := FlareSolverrURL
	FlareSolverrURL = ts.URL
	defer func() { FlareSolverrURL = orig }()

	s := newTestSupplier(t)
	items, err := s.searchKeyword("Some Movie", true)
	if err != nil {
		t.Fatalf("searchKeyword() error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Title != "Some.Movie.2020.1080p" {
		t.Errorf("Title = %q", items[0].Title)
	}
}

// --- error helpers used by downloadSub / searchKeyword ---
// (no extra coverage needed; the FlareSolverr error tests above already
//  exercise the searchKeyword error-return path.)
