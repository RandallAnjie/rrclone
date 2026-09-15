package onedrive

import (
	"testing"

	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/stretchr/testify/assert"
)

func TestGraphOriginFromEndpoint(t *testing.T) {
	assert.Equal(t, "https://graph.microsoft.com", graphOriginFromEndpoint("", regionGlobal))
	assert.Equal(t, "https://microsoftgraph.chinacloudapi.cn", graphOriginFromEndpoint("", regionCN))
	assert.Equal(t, "https://graph.example.com", graphOriginFromEndpoint("https://graph.example.com", regionGlobal))
	assert.Equal(t, "https://graph.example.com", graphOriginFromEndpoint("https://graph.example.com/v1.0", regionCN))
	assert.Equal(t, "https://graph.example.com", graphOriginFromEndpoint("graph.example.com/", regionGlobal))
}

func TestGetRegionURLEndpoint(t *testing.T) {
	m := configmap.Simple{
		"region":   regionGlobal,
		"endpoint": "https://graph.example.com",
	}
	region, graphURL := getRegionURL(m)
	assert.Equal(t, regionGlobal, region)
	assert.Equal(t, "https://graph.example.com/v1.0", graphURL)

	m["tenant_url"] = "https://tenant.sharepoint.com/_api"
	_, graphURL = getRegionURL(m)
	assert.Equal(t, "https://tenant.sharepoint.com/_api/v2.0", graphURL)
}
