package dropbox

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/rclone/rclone/lib/rest"
)

const defaultAPIRoot = "https://api.dropboxapi.com"

// dropboxRoots are the API roots to use with custom endpoints
type dropboxRoots struct {
	api, content, notify string
}

// dropboxCustomRoots works out the roots from the endpoint options.
//
// Blank content and notify endpoints are derived from an "api." host
// by swapping the prefix, otherwise they share the api root (which
// suits a single path-routing proxy). ok is false if nothing is set.
func dropboxCustomRoots(endpoint, contentEndpoint, notifyEndpoint string) (roots dropboxRoots, ok bool) {
	roots = dropboxRoots{
		api:     rest.CanonicalRoot(endpoint),
		content: rest.CanonicalRoot(contentEndpoint),
		notify:  rest.CanonicalRoot(notifyEndpoint),
	}
	if roots == (dropboxRoots{}) {
		return roots, false
	}
	if roots.api == "" {
		roots.api = defaultAPIRoot
	}
	if roots.content == "" {
		roots.content = deriveSiblingHost(roots.api, "api.", "content.")
	}
	if roots.notify == "" {
		roots.notify = deriveSiblingHost(roots.api, "api.", "notify.")
	}
	return roots, true
}

func deriveSiblingHost(root, fromPrefix, toPrefix string) string {
	u, err := url.Parse(root)
	if err != nil || !strings.HasPrefix(u.Host, fromPrefix) {
		return root
	}
	u.Host = toPrefix + strings.TrimPrefix(u.Host, fromPrefix)
	return u.String()
}

// checkEndpoints makes sure the endpoint options are usable URLs
func checkEndpoints(opt *Options) error {
	for _, e := range []struct{ name, value string }{
		{"endpoint", opt.Endpoint},
		{"content_endpoint", opt.ContentEndpoint},
		{"notify_endpoint", opt.NotifyEndpoint},
	} {
		root := rest.CanonicalRoot(e.value)
		if root == "" {
			continue
		}
		u, err := url.Parse(root)
		if err != nil {
			return fmt.Errorf("invalid %s %q: %w", e.name, e.value, err)
		}
		if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("invalid %s %q: want http(s)://host[:port][/path]", e.name, e.value)
		}
	}
	return nil
}

// dropboxTokenURL returns the OAuth token URL to use with endpoint or
// "" for the default. api.dropboxapi.com serves /oauth2/token so a
// proxy for it can refresh tokens too.
func dropboxTokenURL(endpoint string) string {
	root := rest.CanonicalRoot(endpoint)
	if root == "" || root == defaultAPIRoot {
		return ""
	}
	return root + "/oauth2/token"
}

func dropboxURLGeneratorFromOpt(opt *Options) func(hostType, namespace, route string) string {
	roots, ok := dropboxCustomRoots(opt.Endpoint, opt.ContentEndpoint, opt.NotifyEndpoint)
	if !ok {
		return nil
	}
	return dropboxURLGenerator(roots)
}

func dropboxURLGenerator(roots dropboxRoots) func(hostType, namespace, route string) string {
	return func(hostType, namespace, route string) string {
		root := roots.api
		switch hostType {
		case "content":
			root = roots.content
		case "notify":
			root = roots.notify
		}
		return root + "/2/" + namespace + "/" + route
	}
}
