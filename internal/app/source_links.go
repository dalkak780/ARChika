package app

import (
	"net/url"
	"regexp"
	"strings"
)

var numericSourceID = regexp.MustCompile(`^[0-9]+$`)
var galleryToken = regexp.MustCompile(`^[A-Za-z0-9]+$`)

func canonicalSource(provider, raw string) (string, string) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || (u.Port() != "" && u.Port() != "443") {
		return "", ""
	}
	host := strings.ToLower(u.Hostname())
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch provider {
	case "nhentai":
		if host == "nhentai.net" && len(parts) == 2 && parts[0] == "g" && numericSourceID.MatchString(parts[1]) {
			return parts[1], "https://nhentai.net/g/" + parts[1] + "/"
		}
	case "ehentai":
		if (host == "e-hentai.org" || host == "exhentai.org") && len(parts) == 3 && parts[0] == "g" && numericSourceID.MatchString(parts[1]) && galleryToken.MatchString(parts[2]) {
			return parts[1], "https://" + host + "/g/" + parts[1] + "/" + parts[2] + "/"
		}
	case "chaika":
		if host == "panda.chaika.moe" && len(parts) == 2 && (parts[0] == "gallery" || parts[0] == "archive") && numericSourceID.MatchString(parts[1]) {
			return parts[1], "https://panda.chaika.moe/" + parts[0] + "/" + parts[1] + "/"
		}
	}
	return "", ""
}
