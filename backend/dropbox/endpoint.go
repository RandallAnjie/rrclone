package dropbox

import (
	"net/url"
	"strings"

	"github.com/rclone/rclone/lib/rest"
)

func dropboxCustomRoots(endpoint, contentEndpoint string) (apiRoot, contentRoot string, ok bool) {
	apiRoot = rest.CanonicalRoot(endpoint)
	contentRoot = rest.CanonicalRoot(contentEndpoint)
	if apiRoot == "" && contentRoot == "" {
		return "", "", false
	}
	if apiRoot == "" {
		apiRoot = "https://api.dropboxapi.com"
	}
	if contentRoot == "" {
		contentRoot = deriveSiblingHost(apiRoot, "api.", "content.")
	}
	return apiRoot, contentRoot, true
}

func deriveSiblingHost(root, fromPrefix, toPrefix string) string {
	u, err := url.Parse(root)
	if err != nil || !strings.HasPrefix(u.Host, fromPrefix) {
		return root
	}
	u.Host = toPrefix + strings.TrimPrefix(u.Host, fromPrefix)
	return u.String()
}

func dropboxURLGeneratorFromOpt(opt *Options) func(hostType, namespace, route string) string {
	apiRoot, contentRoot, ok := dropboxCustomRoots(opt.Endpoint, opt.ContentEndpoint)
	if !ok {
		return nil
	}
	return dropboxURLGenerator(apiRoot, contentRoot)
}

func dropboxURLGenerator(apiRoot, contentRoot string) func(hostType, namespace, route string) string {
	notifyRoot := deriveSiblingHost(apiRoot, "api.", "notify.")
	return func(hostType, namespace, route string) string {
		root := apiRoot
		switch hostType {
		case "content":
			root = contentRoot
		case "notify":
			root = notifyRoot
		}
		return strings.TrimRight(root, "/") + "/2/" + namespace + "/" + route
	}
}
