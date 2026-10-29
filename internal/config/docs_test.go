package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// settingNames lists every TOML key of t, descending into sections and the
// bucket map's value type.
func settingNames(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
		ft := f.Type
		if ft.Kind() == reflect.Map {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft.PkgPath() == t.PkgPath() {
			out = append(out, settingNames(ft)...)
		}
	}
	return out
}

// Every setting must be documented for people and for AI assistants, so
// new settings cannot be forgotten in either place.
func TestSettingsAreDocumented(t *testing.T) {
	for _, doc := range []string{"../../docs/configuration.md", "../../docs/llms-full.txt"} {
		b, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		for _, name := range settingNames(reflect.TypeOf(Config{})) {
			// `name`, a section ([admin], [buckets.<name>]) or a key after its
			// section in a table cell (`[admin] enabled`).
			if !strings.Contains(text, "`"+name+"`") && !strings.Contains(text, "["+name) && !strings.Contains(text, "] "+name+"`") {
				t.Errorf("%s does not mention the setting %q", doc, name)
			}
		}
	}
}
