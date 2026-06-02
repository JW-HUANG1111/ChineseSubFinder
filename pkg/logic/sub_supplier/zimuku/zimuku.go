package zimuku

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
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
	"github.com/PuerkitoBio/goquery"
	"github.com/sirupsen/logrus"
)

type Supplier struct {
	log            *logrus.Logger
	fileDownloader *file_downloader.FileDownloader
	isAlive        bool
}

func NewSupplier(fileDownloader *file_downloader.FileDownloader) *Supplier {
	sup := Supplier{}
	sup.log = fileDownloader.Log
	sup.fileDownloader = fileDownloader
	sup.isAlive = true
	return &sup
}

func (s *Supplier) CheckAlive() (bool, int64) {
	startT := time.Now()
	httpClient, err := pkg.NewHttpClient()
	if err != nil {
		s.log.Errorln(s.GetSupplierName(), "CheckAlive.NewHttpClient", err)
		return false, 0
	}
	searchPageUrl := settings.Get().AdvancedSettings.SuppliersSettings.Zimuku.GetSearchUrl()
	resp, err := httpClient.R().Get(searchPageUrl)
	if err != nil {
		s.log.Errorln(s.GetSupplierName(), "CheckAlive.Get", err)
		return false, 0
	}
	if resp.StatusCode() != 200 {
		s.log.Errorln(s.GetSupplierName(), "CheckAlive.StatusCode", resp.StatusCode())
		return false, 0
	}
	s.isAlive = true
	return true, time.Since(startT).Milliseconds()
}

func (s *Supplier) IsAlive() bool {
	return s.isAlive
}

func (s *Supplier) OverDailyDownloadLimit() bool {
	limit := settings.Get().AdvancedSettings.SuppliersSettings.Zimuku.DailyDownloadLimit
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
	return common2.SubSiteZiMuKu
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
		searchKeyword = keyWord
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

		// 优先搜索 SxxExx 格式
		searchKeyword := fmt.Sprintf("%s S%02dE%02d", keyWord, episodeInfo.Season, episodeInfo.Episode)
		searchResultItems, err := s.searchKeyword(searchKeyword, false)
		if err != nil || len(searchResultItems) == 0 {
			// 没有则搜索全季
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

// searchKeyword 搜索字幕
func (s *Supplier) searchKeyword(keyword string, isMovie bool) ([]SearchResultItem, error) {
	httpClient, err := pkg.NewHttpClient()
	if err != nil {
		return nil, errors.New("NewHttpClient error:" + err.Error())
	}

	rootUrl := settings.Get().AdvancedSettings.SuppliersSettings.Zimuku.RootUrl
	searchFormat := settings.Get().AdvancedSettings.SuppliersSettings.Zimuku.SearchUrl
	encoded := url.QueryEscape(keyword)
	pageUrl := fmt.Sprintf(rootUrl + searchFormat, encoded)

	resp, err := httpClient.R().Get(pageUrl)
	if err != nil {
		return nil, errors.New("http get error:" + err.Error())
	}

	return s.parseSearchResult(resp.String(), isMovie)
}

// parseSearchResult 解析搜索结果页面
func (s *Supplier) parseSearchResult(html string, isMovie bool) ([]SearchResultItem, error) {
	searchResultItems := make([]SearchResultItem, 0)

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, errors.New("goquery NewDocumentFromReader error:" + err.Error())
	}

	// 找字幕列表
	doc.Find(".sub-item, .sub-list li, .item, .result-item").EachWithBreak(func(i int, selection *goquery.Selection) bool {
		var title string
		var href string

		selection.Find("a").EachWithBreak(func(j int, a *goquery.Selection) bool {
			text := a.Text()
			if len(strings.TrimSpace(text)) > 0 {
				title = text
				href, _ = a.Attr("href")
				return false
			}
			return true
		})

		if title == "" || href == "" {
			return true
		}

		isFullSeason, season, eps, err := decode.GetSeasonAndEpisodeFromSubFileName(title)
		if err != nil {
			s.log.Warningln(s.GetSupplierName(), "decode.GetSeasonAndEpisodeFromSubFileName", err)
			return true
		}

		searchResultItems = append(searchResultItems, SearchResultItem{
			Title:        title,
			IsMovie:      isMovie,
			RUrl:         s.makeAbsoluteUrl(href, settings.Get().AdvancedSettings.SuppliersSettings.Zimuku.RootUrl),
			Season:       season,
			Episode:      eps,
			IsFullSeason: isFullSeason,
		})

		return true
	})

	return searchResultItems, nil
}

// downloadSub 下载字幕
func (s *Supplier) downloadSub(videoFPath, pageUrl string, season, episode int) (*supplier.SubInfo, error) {
	httpClient, err := pkg.NewHttpClient()
	if err != nil {
		return nil, errors.New("NewHttpClient error:" + err.Error())
	}

	resp, err := httpClient.R().Get(pageUrl)
	if err != nil {
		return nil, errors.New("http get error:" + err.Error())
	}

	subInfos := s.parseSubPage(resp.String(), videoFPath, season, episode)
	if len(subInfos) == 0 {
		return nil, errors.New("no subtitle found on page")
	}

	return &subInfos[0], nil
}

// parseSubPage 解析字幕详情页
func (s *Supplier) parseSubPage(html, videoFPath string, season, episode int) []supplier.SubInfo {
	subInfos := make([]supplier.SubInfo, 0)

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return subInfos
	}

	doc.Find("a[href*='.zip'], a[href*='.srt'], a[href*='.ass'], .download a, .btn-download a").EachWithBreak(func(i int, selection *goquery.Selection) bool {
		href, ok := selection.Attr("href")
		if !ok || href == "" {
			return true
		}

		// 相对路径需要拼接
		if !strings.HasPrefix(href, "http") {
			rootUrl := settings.Get().AdvancedSettings.SuppliersSettings.Zimuku.RootUrl
			href = rootUrl + href
		}

		hrefAbs := s.makeAbsoluteUrl(href, settings.Get().AdvancedSettings.SuppliersSettings.Zimuku.RootUrl)
		subInfo := supplier.SubInfo{
			Season:       season,
			Episode:      episode,
			VideoFPath:   videoFPath,
			SupplierName: s.GetSupplierName(),
			Link:         hrefAbs,
			Ext:          getExt(href),
		}

		subInfos = append(subInfos, subInfo)
		return len(subInfos) < 5
	})

	return subInfos
}

// makeAbsoluteUrl 将相对路径转换为绝对 URL
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

// getTotalPage removed as unused

// SearchResultItem 搜索结果项
type SearchResultItem struct {
	Title        string
	IsMovie      bool
	RUrl         string
	Season       int
	Episode      int
	IsFullSeason bool
}

// rootUrl Zimuku 根地址，用于相对路径拼接
var rootUrl = "https://zimuku.org"