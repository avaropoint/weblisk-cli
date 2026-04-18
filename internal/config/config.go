package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds resolved build configuration.
type Config struct {
	Origin           string
	Dist             string
	Port             int      // WL_PORT — dev server port (default: 3000)
	CDN              string   // WL_CDN — if set, importmaps point here instead of /lib/weblisk/
	License          string   // WL_LICENSE — pro license key for downloading pro modules
	BlueprintSources []string // WL_BLUEPRINT_SOURCES — additional blueprint repo URLs
	TemplateSources  []string // WL_TEMPLATE_SOURCES — additional template repo URLs
}

// Vars stores loaded WL_* environment variables.
var Vars = map[string]string{}

// Load reads a .env file from root and merges into os environment.
// Existing env vars take precedence (12-factor).
func Load(root string) error {
	path := filepath.Join(root, ".env")
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])

		// Strip surrounding quotes
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}

		// Process env takes precedence
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
		if strings.HasPrefix(key, "WL_") {
			Vars[key] = val
		}
	}
	return sc.Err()
}

// Resolve returns the merged config with defaults.
func Resolve() Config {
	origin := os.Getenv("WL_ORIGIN")
	if origin == "" {
		origin = "http://localhost:3000"
	}
	dist := os.Getenv("WL_DIST")
	if dist == "" {
		dist = "dist"
	}
	port := 3000
	if p := os.Getenv("WL_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	cdn := os.Getenv("WL_CDN")
	// Strip trailing slash for consistent concatenation
	cdn = strings.TrimRight(cdn, "/")
	license := os.Getenv("WL_LICENSE")

	var blueprintSources []string
	if src := os.Getenv("WL_BLUEPRINT_SOURCES"); src != "" {
		for _, s := range strings.Split(src, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				blueprintSources = append(blueprintSources, s)
			}
		}
	}

	var templateSources []string
	if src := os.Getenv("WL_TEMPLATE_SOURCES"); src != "" {
		for _, s := range strings.Split(src, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				templateSources = append(templateSources, s)
			}
		}
	}

	return Config{Origin: origin, Dist: dist, Port: port, CDN: cdn, License: license, BlueprintSources: blueprintSources, TemplateSources: templateSources}
}
