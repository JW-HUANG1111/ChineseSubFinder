package subhd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ChineseSubFinder/ChineseSubFinder/pkg"

	common2 "github.com/ChineseSubFinder/ChineseSubFinder/pkg/types/common"
	"github.com/ChineseSubFinder/ChineseSubFinder/pkg/types/series"
	"github.com/ChineseSubFinder/ChineseSubFinder/pkg/types/supplier"

	"github.com/ChineseSubFinder/ChineseSubFinder/pkg/decode"
	"github.com/ChineseSubFinder/ChineseSubFinder/pkg/logic/file_downloader"
	"github.com/ChineseSubFinder/ChineseSubFinder/pkg/mix_media_info"
	"github.com/ChineseSubFinder/ChineseSubFinder/pkg/settings"
	"github.com/sirupsen/logrus"
)

// FlareSolverrConfig holds the FlareSolverr connection settings.
// Default address: http://192.168.0.250:8191
// Can be overridden via FLARESOLVERR_URL env var or settings.
var FlareSolverrURL = "http://192.168.0.250:8191"

type Supplier struct {
	log            *logrus.Logger
	fileDownloader *file_downloader.FileDownloader
	isAlive        bool
	httpClient     *http.Client
}

func NewSupplier(fileDownloader *file_downloader.FileDownloader) *Supplier {
	sup := Supplier{}
	sup.log = fileDownloader.Log
	sup.fileDownloader = fileDownloader
	sup.isAlive = true
	sup.httpClient = &http.Client{Timeout: 30 * time.Second}

	// Allow override via env or settings
	if envURL := pkg.GetEnv("FLARESOLVERR_URL", ""); envURL != "" {
		FlareSolverrURL = envURL
	}

	return &sup
}

func (s *Supplier) CheckAlive() (bool, int64) {
	startT := time.Now()

	// Test FlareSolverr with a simple request
	resp, err := s.flareSolverrRequest("GET", settings.Get().AdvancedSettings.SuppliersSettings.SubHD.RootUrl, nil, nil, 0)
	if err != nil {
		s.log.Errorln(s.GetSupplierName(), "CheckAlive.FlareSolverr", err)
		return false, 0
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		s.log.Errorln(s.GetSupplierName(), "CheckAlive.StatusCode", resp.StatusCode)
		return false, 0
	}
	s.isAlive = true
	return true, time.Since(startT).Milliseconds()
}

func (s *Supplier) IsAlive() bool {
	return s.isAlive
}

func (s *Supplier) OverDailyDownloadLimit() bool {
	limit := settings.Get().AdvancedSettings.SuppliersSettings.SubHD.DailyDownloadLimit
	if limit == 0 {
		s.log.Warningln(s.GetSupplierName(), "DailyDownloadLimit is 0, will Skip Download")
		return true
	}
	return false
}

func (s *Supplier) GetLogger() *logrus.Logger {
	return s.log
}

func (s *Supplier) GetSupplierName() string {
	return common2.SubSiteSubHd
}

func (s *Supplier) GetSubListFromFile4Movie(videoFPath string) ([]supplier.SubInfo, error) {
	defer func() {
		s.log.Debugln(s.GetSupplierName(), videoFPath, "End...")
	}()

	s.log.Debugln(s.GetSupplierName(), videoFPath, "Start...")

	outSubInfos := make([]supplier.SubInfo, 0)

	mediaInfo, err := mix_media_info.GetMixMediaInfo(s.fileDownloader.MediaInfoDealers, videoFPath, true)
	if err != nil {
		s.log.Errorln(s.GetSupplierName(), "GetMixMediaInfo", err)
		return nil, err
	}

	keyWord, err := mix_media_info.KeyWordSelect(mediaInfo, videoFPath, true, "cn")
	if err != nil {
		s.log.Errorln(s.GetSupplierName(), "keyWordSelect", err)
		return nil, err
	}

	airTime, err := time.Parse("2006", mediaInfo.Year)
	if err != nil || mediaInfo.Year == "" {
		searchKeyword := keyWord
	} else {
		searchKeyword = fmt.Sprintf("%s %d", keyWord, airTime.Year())
	}
	s.log.Infoln(s.GetSupplierName(), "searchKeyword", searchKeyword)

	searchResultItems, err := s.searchKeyword(searchKeyword, true)
	if err != nil {
		return nil, err
	}
	if len(searchResultItems) == 0 {
		s.log.Infoln(s.GetSupplierName(), searchKeyword, "not found")
		return nil, nil
	}

	for i, item := range searchResultItems {
		if i >= settings.Get().AdvancedSettings.Topic {
			break
		}
		subInfo, err := s.downloadSub(videoFPath, item.RUrl, 0, 0)
		if err != nil {
			s.log.Errorln(s.GetSupplierName(), "downloadSub", err)
			continue
		}
		outSubInfos = append(outSubInfos, *subInfo)
	}

	return outSubInfos, nil
}

func (s *Supplier) GetSubListFromFile4Series(seriesInfo *series.SeriesInfo) ([]supplier.SubInfo, error) {
	defer func() {
		s.log.Debugln(s.GetSupplierName(), seriesInfo.Name, "End...")
	}()

	s.log.Debugln(s.GetSupplierName(), seriesInfo.Name, "Start...")

	outSubInfos := make([]supplier.SubInfo, 0)

	for _, episodeInfo := range seriesInfo.NeedDlEpsKeyList {
		mediaInfo, err := mix_media_info.GetMixMediaInfo(s.fileDownloader.MediaInfoDealers, episodeInfo.FileFullPath, false)
		if err != nil {
			s.log.Errorln(s.GetSupplierName(), "GetMixMediaInfo", err)
			return nil, err
		}

		keyWord, err := mix_media_info.KeyWordSelect(mediaInfo, episodeInfo.FileFullPath, false, "cn")
		if err != nil {
			s.log.Errorln(s.GetSupplierName(), "keyWordSelect", err)
			return nil, err
		}

		// Prioritize SxxExx format
		searchKeyword := fmt.Sprintf("%s S%02dE%02d", keyWord, episodeInfo.Season, episodeInfo.Episode)
		searchResultItems, err := s.searchKeyword(searchKeyword, false)
		if err != nil || len(searchResultItems) == 0 {
			// Fall back to full season search
			searchKeyword = fmt.Sprintf("%s S%02d", keyWord, episodeInfo.Season)
			searchResultItems, err = s.searchKeyword(searchKeyword, false)
			if err != nil || len(searchResultItems) == 0 {
				s.log.Infoln(s.GetSupplierName(), episodeInfo.Season, episodeInfo.Episode, "no sub found")
				continue
			}
		}

		downloadCounter := 0
		for _, item := range searchResultItems {
			if item.Season != episodeInfo.Season {
				continue
			}
			if item.Episode != episodeInfo.Episode && !item.IsFullSeason {
				continue
			}

			subInfo, err := s.downloadSub(episodeInfo.FileFullPath, item.RUrl, item.Season, item.Episode)
			if err != nil {
				s.log.Errorln(s.GetSupplierName(), "downloadSub", err)
				continue
			}
			outSubInfos = append(outSubInfos, *subInfo)
			downloadCounter++
			if downloadCounter >= 5 {
				break
			}
		}
	}

	return outSubInfos, nil
}

func (s *Supplier) GetSubListFromFile4Anime(seriesInfo *series.SeriesInfo) ([]supplier.SubInfo, error) {
	return s.GetSubListFromFile4Series(seriesInfo)
}

// searchKeyword uses FlareSolverr to search for subtitles
func (s *Supplier) searchKeyword(keyword string, isMovie bool) ([]SearchResultItem, error) {
	searchUrl := settings.Get().AdvancedSettings.SuppliersSettings.SubHD.GetSearchUrl()
	encoded := url.QueryEscape(keyword)
	pageUrl := fmt.Sprintf("%s%s", searchUrl, encoded)

	resp, err := s.flareSolverrRequest("GET", pageUrl, nil, nil, 30000)
	if err != nil {
		return nil, errors.New("FlareSolverr request error:" + err.Error())
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("FlareSolverr status %d", resp.StatusCode)
	}

	return s.parseSearchResult(resp.Body, isMovie)
}

// parseSearchResult parses SubHD search HTML via FlareSolverr
func (s *Supplier) parseSearchResult(body io.Reader, isMovie bool) ([]SearchResultItem, error) {
	// Read all content
	content, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	html := string(content)

	searchResultItems := make([]SearchResultItem, 0)

	// SubHD search results use: <a class="link-dark align-middle" href="/a/{id}">{title}
	// Extract all /a/{id} links with their titles
	re := regexp.MustCompile(`href="/a/([a-zA-Z0-9]+)"[^>]*>([^<]+)<`)
	matches := re.FindAllStringSubmatch(html, -1)

	titleRe := regexp.MustCompile(`link-dark align-middle[^>]+href="/a/([a-zA-Z0-9]+)"[^>]*>([^<]+)`)

	for _, match := range matches {
		id := match[1]
		title := strings.TrimSpace(match[2])
		if title == "" {
			continue
		}

		isFullSeason, season, eps, _ := decode.GetSeasonAndEpisodeFromSubFileName(title)
		item := SearchResultItem{
			Title:        title,
			IsMovie:      isMovie,
			RUrl:         "/a/" + id, // relative path, makeAbsoluteUrl will handle
			Season:       season,
			Episode:      eps,
			IsFullSeason: isFullSeason,
		}
		searchResultItems = append(searchResultItems, item)
	}

	return searchResultItems, nil
}

// downloadSub downloads a subtitle from SubHD via FlareSolverr
func (s *Supplier) downloadSub(videoFPath, pageUrl string, season, episode int) (*supplier.SubInfo, error) {
	rootUrl := settings.Get().AdvancedSettings.SuppliersSettings.SubHD.RootUrl

	// Make absolute URL if needed
	if strings.HasPrefix(pageUrl, "/") {
		pageUrl = rootUrl + pageUrl
	}
	detailUrl := pageUrl

	// Get detail page through FlareSolverr to extract sid
	resp, err := s.flareSolverrRequest("GET", detailUrl, nil, nil, 30000)
	if err != nil {
		return nil, errors.New("FlareSolverr request error:" + err.Error())
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("detail page status %d", resp.StatusCode)
	}

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	html := string(content)

	// Extract sid from page (sid="kAlURK" attribute)
	sidRe := regexp.MustCompile(`sid="([a-zA-Z0-9]+)"`)
	sidMatch := sidRe.FindStringSubmatch(html)
	if len(sidMatch) < 2 {
		return nil, errors.New("sid not found on detail page")
	}
	sid := sidMatch[1]

	// Try download via AJAX POST
	postData := fmt.Sprintf("sub_id=%s", sid)
	ajaxUrl := rootUrl + "/ajax/down_ajax"

	ajaxResp, err := s.flareSolverrRequest("POST", ajaxUrl, map[string]string{
		"Content-Type":       "application/x-www-form-urlencoded",
		"X-Requested-With":   "XMLHttpRequest",
		"Referer":            detailUrl,
		"Origin":              rootUrl,
		"User-Agent":         "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0.0.0 Safari/537.36",
	}, &postData, 30000)
	if err != nil {
		return nil, errors.New("FlareSolverr AJAX request error:" + err.Error())
	}
	defer ajaxResp.Body.Close()

	ajaxContent, _ := io.ReadAll(ajaxResp.Body)
	ajaxHtml := string(ajaxContent)

	// Check if we got JSON with success=true and url
	if strings.Contains(ajaxHtml, `"success":true`) && strings.Contains(ajaxHtml, `"url"`) {
		urlRe := regexp.MustCompile(`"url"\s*:\s*"([^"]+)"`)
		urlMatch := urlRe.FindStringSubmatch(ajaxHtml)
		if len(urlMatch) >= 2 {
			downloadUrl := urlMatch[1]
			ext := getExt(downloadUrl)
			return &supplier.SubInfo{
				Season:       season,
				Episode:      episode,
				VideoFPath:   videoFPath,
				SupplierName: s.GetSupplierName(),
				Link:         downloadUrl,
				Ext:          ext,
			}, nil
		}
	}

	// If AJAX failed, return error with the response
	errMsg := strings.TrimSpace(ajaxHtml)
	if len(errMsg) > 100 {
		errMsg = errMsg[:100]
	}
	return nil, errors.New("download failed: " + errMsg)
}

// flareSolverrRequest makes a request through FlareSolverr
// method: "GET" or "POST"
// url: full URL
// headers: optional headers map
// postData: for POST requests, pointer to string data (nil for GET)
// timeoutMs: request timeout in milliseconds
func (s *Supplier) flareSolverrRequest(method, targetUrl string, headers map[string]string, postData *string, timeoutMs int) (*FlareSolverrResponse, error) {
	flareURL := FlareSolverrURL + "/v1"

	reqBody := map[string]interface{}{
		"cmd":        fmt.Sprintf("request.%s", strings.ToLower(method)),
		"url":        targetUrl,
		"maxTimeout": timeoutMs,
	}
	if timeoutMs == 0 {
		reqBody["maxTimeout"] = 30000
	}

	if headers != nil {
		reqHeaders := make(map[string]string)
		for k, v := range headers {
			reqHeaders[k] = v
		}
		reqBody["headers"] = reqHeaders
	}

	if method == "POST" && postData != nil {
		reqBody["postData"] = *postData
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", flareURL, bytes.NewReader(reqJSON))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body.Close()
		return nil, err
	}
	resp.Body.Close()

	var flareResp FlareSolverrResponse
	if err := json.Unmarshal(body, &flareResp); err != nil {
		return nil, err
	}

	if flareResp.Status == "error" {
		return &flareResp, errors.New(flareResp.Message)
	}

	return &flareResp, nil
}

// FlareSolverrResponse represents FlareSolverr API response
type FlareSolverrResponse struct {
	Status         string `json:"status"`
	Message       string `json:"message,omitempty"`
	Session       string `json:"session,omitempty"`
	Solution      struct {
		Url       string            `json:"url"`
		Status    int               `json:"status"`
		Cookies   []interface{}     `json:"cookies"`
		UserAgent string            `json:"userAgent"`
		Headers   map[string]string `json:"headers"`
		Response  string            `json:"response"`
	} `json:"solution"`
}

// makeAbsoluteUrl converts relative URL to absolute
func (s *Supplier) makeAbsoluteUrl(href, rootUrl string) string {
	if strings.HasPrefix(href, "http") {
		return href
	}
	if strings.HasPrefix(href, "/") {
		return rootUrl + href
	}
	return rootUrl + "/" + href
}

func getExt(href string) string {
	lower := strings.ToLower(href)
	if strings.HasSuffix(lower, ".zip") {
		return ".zip"
	}
	if strings.HasSuffix(lower, ".srt") {
		return ".srt"
	}
	if strings.HasSuffix(lower, ".ass") {
		return ".ass"
	}
	if strings.HasSuffix(lower, ".ssa") {
		return ".ssa"
	}
	return ".zip"
}

// SearchResultItem represents a search result entry
type SearchResultItem struct {
	Title        string
	IsMovie      bool
	RUrl         string
	Season       int
	Episode      int
	IsFullSeason bool
}