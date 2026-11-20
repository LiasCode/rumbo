package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The terminal UI shows each kind of thing in a color of its own, which its
// start configuration can change, or leave out altogether. Bold, dim, and
// underline mark what they mark either way.

// Config is the start configuration of the terminal UI. What the user
// chose of it is in rumbo's configuration file, and changes with rumbo
// frontend config tui field=value.
type Config struct {
	// Theme is auto, which shows colors unless NO_COLOR is set; color, which
	// always does; or plain, which shows none.
	Theme string `json:"theme"`
	// The color of each kind of thing: a name, such as cyan or bright-blue;
	// one of the 256 colors, by its number; #rrggbb; or default, the
	// terminal's own.
	Host     string `json:"color_host"`
	Provider string `json:"color_provider"`
	Code     string `json:"color_code"`
	Success  string `json:"color_success"`
	Failure  string `json:"color_failure"`
	Warning  string `json:"color_warning"`
	Progress string `json:"color_progress"`
}

// defaultConfig is what the terminal UI starts with where its configuration
// says nothing.
var defaultConfig = Config{
	Theme: "auto",
	Host:  "cyan", Provider: "magenta", Code: "yellow",
	Success: "green", Failure: "red", Warning: "yellow", Progress: "cyan",
}

// configured reads the configuration registered, over the defaults.
func configured(given json.RawMessage) (Config, error) {
	config := defaultConfig

	if len(given) > 0 {
		if err := json.Unmarshal(given, &config); err != nil {
			return defaultConfig, fmt.Errorf("the configuration of the terminal UI: %w", err)
		}
	}

	return config, nil
}

// theme is the escape sequence each kind of thing is shown with. An empty
// one leaves it in the terminal's own color.
type theme struct {
	host, provider, code, success, failure, warning, progress string
}

// defaultTheme is what the default colors show.
var defaultTheme = theme{
	host: styleCyan, provider: styleMagenta, code: styleYellow,
	success: styleGreen, failure: styleRed, warning: styleYellow, progress: styleCyan,
}

// themeOf works out the theme a configuration asks for. noColor is the
// NO_COLOR environment variable, which turns colors off unless the theme
// says color.
func themeOf(config Config, noColor string) (theme, error) {
	switch config.Theme {
	case "plain":
		return theme{}, nil
	case "auto":
		if noColor != "" {
			return theme{}, nil
		}
	case "color":
	default:
		return theme{}, fmt.Errorf("theme %q is none of auto, color, and plain", config.Theme)
	}

	var chosen theme

	for _, kind := range []struct {
		field, color string
		into         *string
	}{
		{"color_host", config.Host, &chosen.host},
		{"color_provider", config.Provider, &chosen.provider},
		{"color_code", config.Code, &chosen.code},
		{"color_success", config.Success, &chosen.success},
		{"color_failure", config.Failure, &chosen.failure},
		{"color_warning", config.Warning, &chosen.warning},
		{"color_progress", config.Progress, &chosen.progress},
	} {
		color, err := colorOf(kind.color)

		if err != nil {
			return theme{}, fmt.Errorf("%s: %w", kind.field, err)
		}

		*kind.into = color
	}

	return chosen, nil
}

var colorNames = map[string]int{
	"black": 30, "red": 31, "green": 32, "yellow": 33,
	"blue": 34, "magenta": 35, "cyan": 36, "white": 37,
	"gray": 90, "grey": 90,
}

// colorOf turns a color as the configuration names it into the escape
// sequence that shows it.
func colorOf(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))

	switch {
	case name == "" || name == "default":
		return "", nil

	case strings.HasPrefix(name, "bright-"):
		if code, found := colorNames[strings.TrimPrefix(name, "bright-")]; found && code < 90 {
			return fmt.Sprintf(escape+"[%dm", code+60), nil
		}

	case colorNames[name] > 0:
		return fmt.Sprintf(escape+"[%dm", colorNames[name]), nil

	case len(name) == 7 && name[0] == '#':
		red, errRed := strconv.ParseUint(name[1:3], 16, 8)
		green, errGreen := strconv.ParseUint(name[3:5], 16, 8)
		blue, errBlue := strconv.ParseUint(name[5:7], 16, 8)

		if errRed == nil && errGreen == nil && errBlue == nil {
			return fmt.Sprintf(escape+"[38;2;%d;%d;%dm", red, green, blue), nil
		}

	default:
		if number, err := strconv.Atoi(name); err == nil && number >= 0 && number <= 255 {
			return fmt.Sprintf(escape+"[38;5;%dm", number), nil
		}
	}

	return "", fmt.Errorf("%q is not a color: give a name such as cyan or bright-blue, a number up to 255, #rrggbb, or default", name)
}
