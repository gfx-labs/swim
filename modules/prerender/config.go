package prerender

import (
	"regexp"
	"strings"
)

var cfSchemeRegex = regexp.MustCompile("\"scheme\":\"(http|https)\"")

type CrawlerUserAgents []string

func (c CrawlerUserAgents) Contains(x string) bool {
	uas := []string(c)
	if len(uas) == 0 {
		uas = defaultCrawlerUserAgents[:]
	}
	for _, crawlerAgent := range uas {
		if strings.Contains(strings.ToLower(x), strings.ToLower(crawlerAgent)) {
			return true
		}
	}
	return false
}

type FileTypes []string

func (c FileTypes) Contains(x string) bool {
	uas := []string(c)
	if len(uas) == 0 {
		uas = defaultSkippedTypes[:]
	}
	for _, v := range uas {
		if strings.HasSuffix(x, strings.ToLower(v)) {
			return true
		}
	}
	return false
}

var defaultCrawlerUserAgents = [...]string{
	"googlebot",
	"Yahoo! Slurp",
	"bingbot",
	"yandex",
	"baiduspider",
	"facebookexternalhit",
	"twitterbot",
	"rogerbot",
	"linkedinbot",
	"embedly",
	"quora link preview",
	"showyoubot",
	"outbrain",
	"pinterest/0.",
	"developers.google.com/+/web/snippet",
	"slackbot",
	"vkShare",
	"W3C_Validator",
	"redditbot",
	"Applebot",
	"WhatsApp",
	"flipboard",
	"tumblr",
	"bitlybot",
	"SkypeUriPreview",
	"nuzzel",
	"Discordbot",
	"Google Page Speed",
	"Qwantify",
	"pinterestbot",
	"Bitrix link preview",
	"XING-contenttabreceiver",
	"Chrome-Lighthouse",
	"KiwiNewsBot",
}

var defaultSkippedTypes = [...]string{
	".js",
	".css",
	".xml",
	".less",
	".png",
	".jpg",
	".jpeg",
	".gif",
	".pdf",
	".doc",
	".txt",
	".ico",
	".rss",
	".zip",
	".mp3",
	".rar",
	".exe",
	".wmv",
	".doc",
	".avi",
	".ppt",
	".mpg",
	".mpeg",
	".tif",
	".wav",
	".mov",
	".psd",
	".ai",
	".xls",
	".mp4",
	".m4a",
	".swf",
	".dat",
	".dmg",
	".iso",
	".flv",
	".m4v",
	".torrent",
	".woff",
	".ttf",
	".svg",
	".webmanifest",
}
