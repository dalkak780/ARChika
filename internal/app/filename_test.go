package app

import (
	"reflect"
	"sort"
	"testing"
)

// Adapted from LANraragi RegexParse.t (MIT, copyright Difegue 2018).
// Case folding is search-only; basenameTitle preserves every displayed character.
func TestFilenameParsingReferenceFixtures(t *testing.T) {
	for _, tc := range []struct {
		name string
		tags []string
	}{
		{"(NoNe) [Yanyanyo (Yanyo)] Reijo no Rei no... (Blue Archive) [English] [Digital]", []string{"artist:yanyo", "event:none", "group:yanyanyo", "language:english", "parsed:digital", "series:blue archive"}},
		{"[Yanyo] Reijo no Rei no... [english]", []string{"artist:yanyo", "language:english"}},
		{"(C24) [Atomic Diver Henshuubu (Tajima Shinobu, Yoko)] ART DANGER II (Various) [En,Textless]", []string{"artist:tajima shinobu", "artist:yoko", "event:c24", "group:atomic diver henshuubu", "language:en", "language:textless", "series:various"}},
		{"[Crimson Comics (Crimson)] J-Girl. Ecstasy (Black Cat, D.Gray-man, MX0, To Love-Ru) [English]", []string{"artist:crimson", "group:crimson comics", "language:english", "series:black cat", "series:d.gray-man", "series:mx0", "series:to love-ru"}},
		{"[Some_Artist]_Title_With_Underscores_(Some_Series)_[English]", []string{"artist:some artist", "language:english", "series:some series"}},
		{"(C_99)_[Circle_Name_(Artist_Name)]_My_Title_(Series_Name)_[Eng]", []string{"artist:artist name", "event:c 99", "group:circle name", "language:eng", "series:series name"}},
		{"[  Artist  ]   Padded Title   (  Series  ) [  English  ]", []string{"artist:artist", "language:english", "series:series"}},
		{"[Pixiv] 佐々(66526024) 2024.10.19", []string{"parsed:66526024", "parsed:pixiv"}},
		{"[Artist] Title [English] {Team} (Digital) {plain tag, other tag}", []string{"artist:artist", "language:english", "other tag", "parsed:digital", "parsed:team", "plain tag"}},
		{"Just A Plain Title", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := filenameTags(tc.name, "", true)
			sort.Strings(got)
			sort.Strings(tc.tags)
			if !reflect.DeepEqual(got, tc.tags) {
				t.Fatalf("got %v want %v", got, tc.tags)
			}
			_, display := basenameTitle(tc.name+".zip", "zip")
			if display != tc.name {
				t.Fatal("tag parsing rewrote the filename title")
			}
		})
	}
	custom := filenameTags("first|second|Shown", `^(?P<artist>[^|]+)\|(?P<artist2>[^|]+)\|(?P<title>.+)$`, true)
	sort.Strings(custom)
	if !reflect.DeepEqual(custom, []string{"artist:first", "artist:second"}) {
		t.Fatalf("named extension captures %v", custom)
	}
	if tags := filenameTags("[Artist] Title", "", false); len(tags) != 0 {
		t.Fatal("disabled parser extracted tags")
	}
}
