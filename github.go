package rmqa

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// GitHubRelease is a published (or draft) release on GitHub, read through
// the REST API. Assets are downloaded once and kept in memory.
type GitHubRelease struct {
	Repo  string
	Tag   string
	names []string
	sizes map[string]int64
	data  map[string][]byte
}

// GitHubAPI is the API base URL; tests point it at a local server.
var GitHubAPI = "https://api.github.com"

type ghAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Draft   bool      `json:"draft"`
	Assets  []ghAsset `json:"assets"`
}

// FetchGitHub downloads every asset of a release. spec is "owner/repo" for
// the latest release or "owner/repo@tag". A token from GITHUB_TOKEN or
// GH_TOKEN is used when set, which also makes draft releases visible.
func FetchGitHub(spec string) (*GitHubRelease, error) {
	repo, tag, _ := strings.Cut(spec, "@")
	if strings.Count(repo, "/") != 1 || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") {
		return nil, fmt.Errorf("--github wants owner/repo or owner/repo@tag, not %q", spec)
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	api := strings.TrimSuffix(os.Getenv("RMQA_GITHUB_API"), "/")
	if api == "" {
		api = GitHubAPI
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	get := func(url, accept string) (*http.Response, error) {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", accept)
		req.Header.Set("User-Agent", "rmqa")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			resp.Body.Close()
			hint := ""
			if resp.StatusCode == 404 && token == "" {
				hint = " (for a private repository or a draft release, set GITHUB_TOKEN)"
			}
			return nil, fmt.Errorf("GitHub answered %s for %s%s: %s", resp.Status, url, hint, strings.TrimSpace(string(b)))
		}
		return resp, nil
	}
	var rel ghRelease
	if tag == "" {
		resp, err := get(api+"/repos/"+repo+"/releases/latest", "application/vnd.github+json")
		if err != nil {
			return nil, err
		}
		err = json.NewDecoder(resp.Body).Decode(&rel)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
	} else {
		resp, err := get(api+"/repos/"+repo+"/releases/tags/"+tag, "application/vnd.github+json")
		if err != nil {
			// Draft releases have no tag yet; look for one by name.
			list, lerr := get(api+"/repos/"+repo+"/releases?per_page=50", "application/vnd.github+json")
			if lerr != nil {
				return nil, err
			}
			var all []ghRelease
			derr := json.NewDecoder(list.Body).Decode(&all)
			list.Body.Close()
			if derr != nil {
				return nil, err
			}
			found := false
			for _, r := range all {
				if r.TagName == tag {
					rel, found = r, true
					break
				}
			}
			if !found {
				return nil, err
			}
		} else {
			err = json.NewDecoder(resp.Body).Decode(&rel)
			resp.Body.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	g := &GitHubRelease{Repo: repo, Tag: rel.TagName, sizes: map[string]int64{}, data: map[string][]byte{}}
	for _, a := range rel.Assets {
		resp, err := get(a.URL, "application/octet-stream")
		if err != nil {
			return nil, fmt.Errorf("downloading %s: %w", a.Name, err)
		}
		b, err := readLimited(resp.Body, MaxArchiveBytes)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("downloading %s: %w", a.Name, err)
		}
		g.names = append(g.names, a.Name)
		g.sizes[a.Name] = int64(len(b))
		g.data[a.Name] = b
	}
	sort.Strings(g.names)
	return g, nil
}

func (g *GitHubRelease) Names() []string        { return g.names }
func (g *GitHubRelease) Size(name string) int64 { return g.sizes[name] }
func (g *GitHubRelease) Read(name string) ([]byte, error) {
	b, ok := g.data[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return b, nil
}
func (g *GitHubRelease) Describe() string { return "github.com/" + g.Repo + " release " + g.Tag }
