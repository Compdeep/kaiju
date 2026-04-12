// Package skillhub provides a client for the ClawHub skill registry.
// Skills are markdown-based instruction packages that teach the executive
// how to use tools for specific domains (research, browser automation, etc).
// Compatible with OpenClaw's ClawHub format.
package skillhub

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DefaultRegistry is the default ClawHub registry URL.
const DefaultRegistry = "https://clawhub.ai"

/*
 * Client talks to the ClawHub API.
 * desc: HTTP client for fetching skill metadata and downloading skill packages from a ClawHub-compatible registry.
 */
type Client struct {
	registry string
	http     *http.Client
}

/*
 * NewClient creates a ClawHub client.
 * desc: Initializes a Client pointed at the given registry URL, falling back to DefaultRegistry if empty.
 * param: registry - the ClawHub registry base URL; empty uses the default
 * return: a configured Client
 */
func NewClient(registry string) *Client {
	if registry == "" {
		registry = DefaultRegistry
	}
	return &Client{
		registry: strings.TrimRight(registry, "/"),
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

/*
 * SkillInfo is the response from GET /api/v1/skills/{slug}.
 * desc: Contains skill metadata, latest version info, OS requirements, and owner details.
 */
type SkillInfo struct {
	Skill struct {
		Slug        string `json:"slug"`
		DisplayName string `json:"displayName"`
		Summary     string `json:"summary"`
		Tags        struct {
			Latest string `json:"latest"`
		} `json:"tags"`
		Stats struct {
			Downloads   int `json:"downloads"`
			Installs    int `json:"installsAllTime"`
			Stars       int `json:"stars"`
			Versions    int `json:"versions"`
		} `json:"stats"`
	} `json:"skill"`
	LatestVersion struct {
		Version   string `json:"version"`
		Changelog string `json:"changelog"`
		License   string `json:"license"`
	} `json:"latestVersion"`
	Metadata struct {
		OS []string `json:"os"`
	} `json:"metadata"`
	Owner struct {
		Handle      string `json:"handle"`
		DisplayName string `json:"displayName"`
	} `json:"owner"`
}

/*
 * MetaJSON is the _meta.json file inside a skill zip.
 * desc: Contains the owner ID, slug, version, and publish timestamp for an installed skill.
 */
type MetaJSON struct {
	OwnerID     string `json:"ownerId"`
	Slug        string `json:"slug"`
	Version     string `json:"version"`
	PublishedAt int64  `json:"publishedAt"`
}

/*
 * Info fetches skill metadata from the registry.
 * desc: Sends a GET request to the registry API and decodes the skill info response.
 * param: slug - the unique skill identifier
 * return: the SkillInfo and any error from the HTTP request or JSON decoding
 */
func (c *Client) Info(slug string) (*SkillInfo, error) {
	resp, err := c.http.Get(c.registry + "/api/v1/skills/" + slug)
	if err != nil {
		return nil, fmt.Errorf("fetch skill info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("skill %q not found (HTTP %d)", slug, resp.StatusCode)
	}

	var info SkillInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("parse skill info: %w", err)
	}
	return &info, nil
}

/*
 * Install downloads a skill zip and extracts it to destDir/slug/.
 * desc: Fetches the skill archive from the registry, extracts all files (with zip-slip protection), and reads the version from _meta.json.
 * param: slug - the unique skill identifier to download
 * param: destDir - the parent directory where the skill folder will be created
 * return: the installed version string, list of extracted filenames, and any error
 */
func (c *Client) Install(slug, destDir string) (string, []string, error) {
	// Download zip
	resp, err := c.http.Get(c.registry + "/api/v1/download?slug=" + slug)
	if err != nil {
		return "", nil, fmt.Errorf("download skill: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", nil, fmt.Errorf("download failed (HTTP %d)", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("read download: %w", err)
	}

	// Parse zip
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", nil, fmt.Errorf("invalid zip: %w", err)
	}

	// Extract to destDir/slug/
	skillDir := filepath.Join(destDir, slug)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return "", nil, fmt.Errorf("create skill dir: %w", err)
	}

	var files []string
	var version string

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		// Prevent zip slip
		name := filepath.Base(f.Name)
		target := filepath.Join(skillDir, name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(skillDir)) {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			continue
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			continue
		}
		io.Copy(out, rc)
		out.Close()
		rc.Close()
		files = append(files, name)

		// Read version from _meta.json
		if name == "_meta.json" {
			if metaData, err := os.ReadFile(target); err == nil {
				var meta MetaJSON
				if json.Unmarshal(metaData, &meta) == nil {
					version = meta.Version
				}
			}
		}
	}

	return version, files, nil
}

/*
 * InstalledVersion reads the version from an installed skill's _meta.json.
 * desc: Parses the _meta.json in the given skill directory and returns the version string.
 * param: skillDir - path to the installed skill directory
 * return: the version string, or empty if not found or not a ClawHub skill
 */
func InstalledVersion(skillDir string) string {
	data, err := os.ReadFile(filepath.Join(skillDir, "_meta.json"))
	if err != nil {
		return ""
	}
	var meta MetaJSON
	if json.Unmarshal(data, &meta) != nil {
		return ""
	}
	return meta.Version
}

/*
 * InstalledSlug reads the slug from an installed skill's _meta.json.
 * desc: Parses the _meta.json in the given skill directory and returns the slug identifier.
 * param: skillDir - path to the installed skill directory
 * return: the slug string, or empty if not found
 */
func InstalledSlug(skillDir string) string {
	data, err := os.ReadFile(filepath.Join(skillDir, "_meta.json"))
	if err != nil {
		return ""
	}
	var meta MetaJSON
	if json.Unmarshal(data, &meta) != nil {
		return ""
	}
	return meta.Slug
}

/*
 * CheckBins verifies that required binaries are available on PATH.
 * desc: Iterates over the given binary names and checks each with exec.LookPath.
 * param: bins - list of binary names to check
 * return: a list of binary names that were not found on PATH
 */
func CheckBins(bins []string) []string {
	var missing []string
	for _, bin := range bins {
		if _, err := exec.LookPath(bin); err != nil {
			missing = append(missing, bin)
		}
	}
	return missing
}
