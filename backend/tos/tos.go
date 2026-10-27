// Package tos provides a dedicated Volcengine TOS remote that wraps the S3 backend.
package tos

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"github.com/rclone/rclone/backend/s3"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/lib/encoder"
)

var tosRegionRe = regexp.MustCompile(`(?i)^tos-s3-([a-z0-9-]+)\.(?:i?volces)\.com$`)

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "tos",
		Description: "Volcengine TOS (火山引擎对象存储)",
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name:    "env_auth",
			Help:    "Get credentials from runtime (environment variables).\n\nOnly applies if access_key_id and secret_access_key is blank.",
			Default: false,
			Examples: []fs.OptionExample{{
				Value: "false",
				Help:  "Enter credentials in the next step.",
			}, {
				Value: "true",
				Help:  "Get credentials from the environment.",
			}},
		}, {
			Name:      "access_key_id",
			Help:      "Access Key ID from the Volcengine console.",
			Sensitive: true,
		}, {
			Name:      "secret_access_key",
			Help:      "Secret Access Key from the Volcengine console.",
			Sensitive: true,
		}, {
			Name: "endpoint",
			Help: "S3-compatible endpoint for TOS.\n\nUse tos-s3-<region>.volces.com. The region is also used to sign requests.",
			Examples: []fs.OptionExample{
				{Value: "tos-s3-cn-beijing.volces.com", Help: "China North 2 (Beijing)"},
				{Value: "tos-s3-cn-shanghai.volces.com", Help: "China East 2 (Shanghai)"},
				{Value: "tos-s3-cn-guangzhou.volces.com", Help: "China South 1 (Guangzhou)"},
				{Value: "tos-s3-cn-hongkong.volces.com", Help: "China Hong Kong"},
				{Value: "tos-s3-ap-southeast-1.volces.com", Help: "Asia Pacific Southeast (Johor)"},
			},
		}, {
			Name: "region",
			Help: "TOS region used for SigV4 signing, for example cn-beijing.\n\nLeave blank to infer it from endpoint.",
			Examples: []fs.OptionExample{
				{Value: "cn-beijing", Help: "China North 2 (Beijing)"},
				{Value: "cn-shanghai", Help: "China East 2 (Shanghai)"},
				{Value: "cn-guangzhou", Help: "China South 1 (Guangzhou)"},
				{Value: "cn-hongkong", Help: "China Hong Kong"},
				{Value: "ap-southeast-1", Help: "Asia Pacific Southeast (Johor)"},
			},
		}, {
			Name: "location_constraint",
			Help: "Location constraint used when creating buckets. Leave blank to use region.",
		}, {
			Name: "acl",
			Help: "Canned ACL used when creating buckets and storing or copying objects.",
			Examples: []fs.OptionExample{{
				Value: "private",
				Help:  "Owner gets FULL_CONTROL. No one else has access rights (default).",
			}, {
				Value: "public-read",
				Help:  "Owner gets FULL_CONTROL. The AllUsers group gets READ access.",
			}},
		}, {
			Name: "storage_class",
			Help: "Storage class to use when storing new objects.",
			Examples: []fs.OptionExample{{
				Value: "",
				Help:  "Default",
			}, {
				Value: "STANDARD",
				Help:  "Standard storage",
			}, {
				Value: "IA",
				Help:  "Infrequent access",
			}, {
				Value: "INTELLIGENT_TIERING",
				Help:  "Intelligent tiering",
			}, {
				Value: "ARCHIVE_FR",
				Help:  "Archive flash restore",
			}, {
				Value: "COLD_ARCHIVE",
				Help:  "Cold archive",
			}},
		}, {
			Name:     config.ConfigEncoding,
			Help:     config.ConfigEncodingHelp,
			Advanced: true,
			Default:  encoder.EncodeInvalidUtf8 | encoder.EncodeSlash | encoder.EncodeDot,
		}},
	})
}

// regionFromEndpoint returns the TOS region encoded in an S3 endpoint host.
func regionFromEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	host := endpoint
	if strings.Contains(host, "://") {
		if u, err := url.Parse(host); err == nil && u.Host != "" {
			host = u.Host
		}
	}
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	m := tosRegionRe.FindStringSubmatch(host)
	if len(m) != 2 {
		return ""
	}
	return strings.ToLower(m[1])
}

// NewFs constructs an Fs for Volcengine TOS using the S3 backend.
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	if v, ok := m.Get(fs.ConfigProvider); !ok || v == "" {
		m.Set(fs.ConfigProvider, "Volcengine")
	}
	if region, _ := m.Get("region"); strings.TrimSpace(region) == "" {
		if ep, _ := m.Get("endpoint"); ep != "" {
			if inferred := regionFromEndpoint(ep); inferred != "" {
				m.Set("region", inferred)
			}
		}
	}
	if loc, _ := m.Get("location_constraint"); strings.TrimSpace(loc) == "" {
		if region, _ := m.Get("region"); region != "" {
			m.Set("location_constraint", region)
		}
	}
	return s3.NewFs(ctx, name, root, m)
}
