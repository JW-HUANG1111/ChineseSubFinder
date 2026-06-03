//go:build realtest

package subhd

import (
	"testing"
	"time"

	"github.com/ChineseSubFinder/ChineseSubFinder/pkg/cache_center"
	"github.com/ChineseSubFinder/ChineseSubFinder/pkg/logic/file_downloader"
	"github.com/ChineseSubFinder/ChineseSubFinder/pkg/settings"
	"github.com/sirupsen/logrus"
)

// realTestSupplier builds a Supplier that talks to a *real* FlareSolverr + subhd.tv.
// Run with: go test -tags realtest -v -run TestRealSubhd -count=1 ./pkg/logic/sub_supplier/subhd/...
func realTestSupplier(t *testing.T) *Supplier {
	t.Helper()
	dir := t.TempDir()
	settings.SetConfigRootPath(dir)
	_ = settings.Get()
	log := logrus.New()
	log.SetLevel(logrus.WarnLevel)
	cache := cache_center.NewCacheCenter("realtest-subhd", log)
	fd := file_downloader.NewFileDownloader(cache)
	return NewSupplier(fd)
}

// TestRealCheckAlive — pings the actual FlareSolverr at 192.168.0.250:8191,
// which in turn tries to reach subhd.tv.
func TestRealCheckAlive(t *testing.T) {
	if FlareSolverrURL == "http://192.168.0.250:8191" {
		t.Logf("FlareSolverrURL = %s", FlareSolverrURL)
	}
	s := realTestSupplier(t)
	t.Logf("FlareSolverrURL = %s", FlareSolverrURL)
	t.Logf("SubHD.RootUrl  = %s", settings.Get().AdvancedSettings.SuppliersSettings.SubHD.RootUrl)
	t.Logf("SubHD.SearchUrl= %s", settings.Get().AdvancedSettings.SuppliersSettings.SubHD.SearchUrl)

	start := time.Now()
	alive, ms := s.CheckAlive()
	t.Logf("CheckAlive() = %v, took %d ms", alive, ms)
	t.Logf("wall time: %v", time.Since(start))

	if !alive {
		t.Fatalf("CheckAlive returned false — FlareSolverr couldn't reach subhd.tv")
	}
	if !s.IsAlive() {
		t.Error("IsAlive() = false after successful CheckAlive")
	}
}

// TestRealSearchKeyword — real call: subhd.go -> FlareSolverr -> subhd.tv
func TestRealSearchKeyword(t *testing.T) {
	s := realTestSupplier(t)
	searchUrl := settings.Get().AdvancedSettings.SuppliersSettings.SubHD.GetSearchUrl()
	t.Logf("SubHD.GetSearchUrl() = %q", searchUrl)
	items, err := s.searchKeyword("Inception 2010", true)
	if err != nil {
		t.Fatalf("searchKeyword() error: %v", err)
	}
	t.Logf("found %d items for 'Inception 2010'", len(items))
	for i, it := range items[:minOf(5, len(items))] {
		t.Logf("  [%d] %q  RUrl=%s  S/E=%d/%d full=%v",
			i, it.Title, it.RUrl, it.Season, it.Episode, it.IsFullSeason)
	}
	if len(items) == 0 {
		t.Fatal("expected ≥1 search result, got 0 — HTML selector likely mismatched")
	}
}

func minOf(a, b int) int {
	if a < b {
		return a
	}
	return b
}
