package tui

import (
	"encoding/json"
	"strings"
	"testing"
)

// The configuration registered goes over the defaults, and what is in force
// has every field, for the host to keep.
func TestTheConfigurationGoesOverTheDefaults(t *testing.T) {
	config, err := configured(json.RawMessage(`{"theme":"plain","color_code":"#d7875f"}`))

	if err != nil || config.Theme != "plain" || config.Code != "#d7875f" || config.Host != "cyan" {
		t.Fatalf("configured = %+v, %v", config, err)
	}

	if config, _ := configured(nil); config != defaultConfig {
		t.Fatalf("nothing registered = %+v", config)
	}

	inForce, _ := json.Marshal(defaultConfig)

	if !strings.Contains(string(inForce), `"theme":"auto"`) || !strings.Contains(string(inForce), `"color_progress":"cyan"`) {
		t.Fatalf("in force = %s", inForce)
	}

	// A color of the 256, written in YAML as a number, is the text it is.
	if numbered, err := configured(json.RawMessage(`{"color_code":214}`)); err != nil || numbered.Code != "214" {
		t.Fatalf("a color written as a number = %+v, %v", numbered, err)
	}

	if _, err := configured(json.RawMessage(`{"theme":true}`)); err == nil {
		t.Fatal("a theme that is no text was taken")
	}

	// A field it does not have, such as a misspelled one, stops it, saying
	// which and how to take it out.
	_, err = configured(json.RawMessage(`{"theme":"plain","colr_host":"red"}`))

	if err == nil || !strings.Contains(err.Error(), `"colr_host"`) || !strings.Contains(err.Error(), "rumbo frontend set tui colr_host=") || !strings.Contains(err.Error(), "color_host") {
		t.Fatalf("a field it does not have: err = %v", err)
	}
}

// The default colors are the default theme; plain has none, and so has
// auto when NO_COLOR is set, unless the theme says color.
func TestTheThemeIsWhatTheConfigurationSays(t *testing.T) {
	colored := defaultConfig
	colored.Theme = "color"

	for _, test := range []struct {
		config  Config
		noColor string
		want    theme
	}{
		{defaultConfig, "", defaultTheme},
		{defaultConfig, "1", theme{}},
		{colored, "1", defaultTheme},
		{Config{Theme: "plain", Code: "red"}, "", theme{}},
	} {
		got, err := themeOf(test.config, test.noColor)

		if err != nil || got != test.want {
			t.Errorf("themeOf(%+v, %q) = %+v, %v", test.config, test.noColor, got, err)
		}
	}

	wrong := defaultConfig
	wrong.Code = "chartreuse"

	if _, err := themeOf(wrong, ""); err == nil || !strings.Contains(err.Error(), "color_code") {
		t.Fatalf("a color that is none: %v", err)
	}

	if _, err := themeOf(Config{Theme: "dark"}, ""); err == nil {
		t.Fatal("a theme that is none was taken")
	}
}

func TestColorsAreNamedFourWays(t *testing.T) {
	for name, want := range map[string]string{
		"cyan":        escape + "[36m",
		"Bright-Blue": escape + "[94m",
		"gray":        escape + "[90m",
		"173":         escape + "[38;5;173m",
		"#d7875f":     escape + "[38;2;215;135;95m",
		"default":     "",
		"":            "",
	} {
		if got, err := colorOf(name); err != nil || got != want {
			t.Errorf("colorOf(%q) = %q, %v; want %q", name, got, err, want)
		}
	}

	for _, name := range []string{"256", "#12345", "#gg0000", "bright-gray", "teal"} {
		if _, err := colorOf(name); err == nil {
			t.Errorf("colorOf(%q) was taken", name)
		}
	}
}
