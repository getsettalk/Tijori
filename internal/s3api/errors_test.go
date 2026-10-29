package s3api

import "testing"

func TestWantsJSON(t *testing.T) {
	cases := map[string]bool{
		"":                                  false,
		"*/*":                               false,
		"application/xml":                   false,
		"application/json":                  true,
		"application/json, text/plain, */*": true, // axios default
		"application/xml, application/json;q=0.5":                         false,
		"application/json;q=0.9, text/xml;q=0.8":                          true,
		"application/problem+json":                                        true,
		"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8": false, // browsers
	}
	for accept, want := range cases {
		if got := WantsJSON(accept); got != want {
			t.Errorf("WantsJSON(%q) = %v, want %v", accept, got, want)
		}
	}
}
