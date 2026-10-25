package app

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const filenameParserVersion = "2"

var filenamePattern = regexp.MustCompile(`^(?:\((?P<event>[^([]+)\))?\s*(?:\[(?P<artist>[^]]+)\])?\s*(?P<title>[^([]*)\s*(?:\((?P<series>[^([)]+)\))?\s*(?:\[(?P<language>[^]]+)\])?(?P<tail>.*)$`)
var artistGroupPattern = regexp.MustCompile(`^(.*?)\s+\((.*)\)\s*$`)
var tailPattern = regexp.MustCompile(`\(([^)]*)\)|\[([^]]*)\]|\{([^}]*)\}`)
var trailingPattern = regexp.MustCompile(`\{([^}]*)\}\s*$`)
var namespaceSuffix = regexp.MustCompile(`[0-9]+$`)

func filenameTags(name, override string, enabled bool) []string {
	tags := []string{}
	if !enabled {
		return tags
	}
	re := filenamePattern
	if override != "" {
		r, e := regexp.Compile(override)
		if e != nil {
			return tags
		}
		re = r
	}
	m := re.FindStringSubmatch(strings.ReplaceAll(name, "_", " "))
	if m == nil {
		return tags
	}
	add := func(namespace, value string) {
		for _, part := range strings.Split(value, ",") {
			v := normalizeTag(part)
			if v == "" {
				continue
			}
			ns := namespace
			_, number := strconv.ParseFloat(v, 64)
			extraneous := false
			switch v {
			case "uncensored", "decensored", "ongoing", "pixiv", "twitter", "fanbox", "cosplay", "digital":
				extraneous = true
			}
			if namespace != "event" && (number == nil || namespace != "" && extraneous) {
				ns = "parsed"
			}
			if ns != "" {
				v = ns + ":" + v
			}
			tags = append(tags, v)
		}
	}
	for i, k := range re.SubexpNames() {
		if i == 0 || i >= len(m) || m[i] == "" {
			continue
		}
		k = namespaceSuffix.ReplaceAllString(k, "")
		value := strings.TrimSpace(m[i])
		switch k {
		case "title", "":
			continue
		case "tail":
			if trailing := trailingPattern.FindStringSubmatchIndex(value); trailing != nil {
				add("", value[trailing[2]:trailing[3]])
				value = value[:trailing[0]]
			}
			for _, captures := range tailPattern.FindAllStringSubmatch(value, -1) {
				for _, v := range captures[1:] {
					if v != "" {
						add("parsed", v)
					}
				}
			}
		case "artist":
			if group := artistGroupPattern.FindStringSubmatch(value); group != nil {
				tags = append(tags, "group:"+normalizeTag(group[1]))
				value = group[2]
			}
			add("artist", value)
		case "tag", "tags":
			add("", value)
		default:
			add(k, value)
		}
	}
	sort.Strings(tags)
	return dedupStrings(tags)
}
func normalizeTag(s string) string {
	s = norm.NFC.String(s)
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return s
}

// naturalKey is indexed once, not computed by a SQL collation for every result.
func naturalKey(s string) string {
	var b strings.Builder
	r := []rune(strings.ToLower(s))
	for i := 0; i < len(r); {
		if r[i] >= '0' && r[i] <= '9' {
			j := i
			for j < len(r) && r[j] >= '0' && r[j] <= '9' {
				j++
			}
			digits := strings.TrimLeft(string(r[i:j]), "0")
			if digits == "" {
				digits = "0"
			}
			b.WriteRune(0x01)
			n := len(digits)
			const hex = "0123456789abcdef"
			for shift := 12; shift >= 0; shift -= 4 {
				b.WriteByte(hex[(n>>shift)&15])
			}
			b.WriteString(digits)
			i = j
		} else {
			b.WriteRune(r[i])
			i++
		}
	}
	return b.String()
}
