// Package oss provides a dedicated Alibaba Cloud OSS remote that wraps the S3 backend.
package oss

import (
	"context"

	"github.com/rclone/rclone/backend/s3"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/lib/encoder"
)

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "oss",
		Description: "Alibaba Cloud OSS (阿里云对象存储)",
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
			Help:      "Access Key ID from the Alibaba Cloud console.",
			Sensitive: true,
		}, {
			Name:      "secret_access_key",
			Help:      "Access Key Secret from the Alibaba Cloud console.",
			Sensitive: true,
		}, {
			Name: "endpoint",
			Help: "Endpoint for the OSS API.\n\nPick the region that matches the bucket, or the accelerate endpoint.",
			Examples: []fs.OptionExample{
				{Value: "oss-cn-hangzhou.aliyuncs.com", Help: "East China 1 (Hangzhou)"},
				{Value: "oss-cn-shanghai.aliyuncs.com", Help: "East China 2 (Shanghai)"},
				{Value: "oss-cn-qingdao.aliyuncs.com", Help: "North China 1 (Qingdao)"},
				{Value: "oss-cn-beijing.aliyuncs.com", Help: "North China 2 (Beijing)"},
				{Value: "oss-cn-zhangjiakou.aliyuncs.com", Help: "North China 3 (Zhangjiakou)"},
				{Value: "oss-cn-huhehaote.aliyuncs.com", Help: "North China 5 (Hohhot)"},
				{Value: "oss-cn-wulanchabu.aliyuncs.com", Help: "North China 6 (Ulanqab)"},
				{Value: "oss-cn-shenzhen.aliyuncs.com", Help: "South China 1 (Shenzhen)"},
				{Value: "oss-cn-heyuan.aliyuncs.com", Help: "South China 2 (Heyuan)"},
				{Value: "oss-cn-guangzhou.aliyuncs.com", Help: "South China 3 (Guangzhou)"},
				{Value: "oss-cn-chengdu.aliyuncs.com", Help: "West China 1 (Chengdu)"},
				{Value: "oss-cn-hongkong.aliyuncs.com", Help: "Hong Kong"},
				{Value: "oss-us-west-1.aliyuncs.com", Help: "US West 1 (Silicon Valley)"},
				{Value: "oss-us-east-1.aliyuncs.com", Help: "US East 1 (Virginia)"},
				{Value: "oss-ap-southeast-1.aliyuncs.com", Help: "Southeast Asia 1 (Singapore)"},
				{Value: "oss-ap-southeast-2.aliyuncs.com", Help: "Asia Pacific Southeast 2 (Sydney)"},
				{Value: "oss-ap-southeast-3.aliyuncs.com", Help: "Southeast Asia 3 (Kuala Lumpur)"},
				{Value: "oss-ap-southeast-5.aliyuncs.com", Help: "Asia Pacific Southeast 5 (Jakarta)"},
				{Value: "oss-ap-northeast-1.aliyuncs.com", Help: "Asia Pacific Northeast 1 (Japan)"},
				{Value: "oss-ap-south-1.aliyuncs.com", Help: "Asia Pacific South 1 (Mumbai)"},
				{Value: "oss-eu-central-1.aliyuncs.com", Help: "Central Europe 1 (Frankfurt)"},
				{Value: "oss-eu-west-1.aliyuncs.com", Help: "West Europe (London)"},
				{Value: "oss-me-east-1.aliyuncs.com", Help: "Middle East 1 (Dubai)"},
				{Value: "oss-accelerate.aliyuncs.com", Help: "Global Accelerate"},
				{Value: "oss-accelerate-overseas.aliyuncs.com", Help: "Global Accelerate (outside mainland China)"},
			},
		}, {
			Name: "region",
			Help: "Region to sign requests with, for example cn-hangzhou.\n\nLeave blank to use the default. The endpoint already selects the bucket region.",
		}, {
			Name: "location_constraint",
			Help: "Location constraint used when creating buckets. Leave blank if unsure.",
		}, {
			Name: "acl",
			Help: "Canned ACL used when creating buckets and storing or copying objects.",
			Examples: []fs.OptionExample{{
				Value: "private",
				Help:  "Owner gets FULL_CONTROL. No one else has access rights (default).",
			}, {
				Value: "public-read",
				Help:  "Owner gets FULL_CONTROL. The AllUsers group gets READ access.",
			}, {
				Value: "public-read-write",
				Help:  "Owner gets FULL_CONTROL. The AllUsers group gets READ and WRITE access.",
			}},
		}, {
			Name: "storage_class",
			Help: "Storage class to use when storing new objects.",
			Examples: []fs.OptionExample{{
				Value: "",
				Help:  "Default",
			}, {
				Value: "STANDARD",
				Help:  "Standard storage class",
			}, {
				Value: "STANDARD_IA",
				Help:  "Infrequent access",
			}, {
				Value: "GLACIER",
				Help:  "Archive (Glacier)",
			}},
		}, {
			Name:     config.ConfigEncoding,
			Help:     config.ConfigEncodingHelp,
			Advanced: true,
			Default:  encoder.EncodeInvalidUtf8 | encoder.EncodeSlash | encoder.EncodeDot,
		}},
	})
}

// NewFs constructs an Fs for Alibaba Cloud OSS using the S3 backend.
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	if v, ok := m.Get(fs.ConfigProvider); !ok || v == "" {
		m.Set(fs.ConfigProvider, "Alibaba")
	}
	return s3.NewFs(ctx, name, root, m)
}
