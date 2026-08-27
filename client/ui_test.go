package main

import "testing"

func TestElideKeepsTheInformativeEnd(t *testing.T) {
	cases := []struct {
		name, in string
		width    int
		wantHead string // what must survive at the start, "" if the tail matters
		wantTail string
	}{
		{
			// "i/o timeout" contains a slash but is not a path; eliding from
			// the left would throw away the explanation
			name:     "error message keeps its explanation",
			in:       "connected, but the upgrade went unanswered for 10s: read tcp 1.2.3.4:1->5.6.7.8:443: i/o timeout",
			width:    40,
			wantHead: "connected, but the upgrade",
		},
		{
			name:     "unix path keeps its file name",
			in:       "/very/long/directory/that/goes/on/and/on/beacon/config.json",
			width:    24,
			wantTail: "config.json",
		},
		{
			name:     "windows path keeps its file name",
			in:       `C:\Program Files\beacon\somewhere\deep\beacon.exe`,
			width:    20,
			wantTail: "beacon.exe",
		},
		{
			name:     "short text is untouched",
			in:       "all good",
			width:    40,
			wantHead: "all good",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := elide(tc.in, tc.width)
			if len([]rune(got)) > tc.width {
				t.Fatalf("%q is wider than %d", got, tc.width)
			}
			if tc.wantHead != "" && !hasPrefix(got, tc.wantHead) {
				t.Fatalf("got %q, expected it to start with %q", got, tc.wantHead)
			}
			if tc.wantTail != "" && !hasSuffix(got, tc.wantTail) {
				t.Fatalf("got %q, expected it to end with %q", got, tc.wantTail)
			}
		})
	}
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
func hasSuffix(s, p string) bool { return len(s) >= len(p) && s[len(s)-len(p):] == p }
