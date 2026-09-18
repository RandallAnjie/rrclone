package tos

import "testing"

func TestRegionFromEndpoint(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"tos-s3-cn-beijing.volces.com", "cn-beijing"},
		{"https://tos-s3-cn-shanghai.volces.com", "cn-shanghai"},
		{"https://tos-s3-ap-southeast-1.volces.com:443", "ap-southeast-1"},
		{"tos-s3-cn-guangzhou.ivolces.com", "cn-guangzhou"},
		{"https://tos-s3-cn-hongkong.volces.com/", "cn-hongkong"},
		{"oss-cn-hangzhou.aliyuncs.com", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := regionFromEndpoint(tt.in); got != tt.want {
			t.Errorf("regionFromEndpoint(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
