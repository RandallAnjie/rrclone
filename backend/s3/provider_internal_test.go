package s3

import "testing"

func TestLoadVolcengineProvider(t *testing.T) {
	p := loadProvider("Volcengine")
	if p == nil {
		t.Fatal("Volcengine provider YAML was not embedded")
	}
	if p.Name != "Volcengine" {
		t.Fatalf("provider name = %q", p.Name)
	}
	if p.Endpoint == nil {
		t.Fatal("expected TOS endpoints")
	}
	if _, ok := p.Endpoint.Get("tos-s3-cn-beijing.volces.com"); !ok {
		t.Fatal("missing Beijing S3 endpoint")
	}
}

func TestLoadAlibabaProvider(t *testing.T) {
	p := loadProvider("Alibaba")
	if p == nil {
		t.Fatal("Alibaba provider YAML was not embedded")
	}
	if _, ok := p.Endpoint.Get("oss-cn-hangzhou.aliyuncs.com"); !ok {
		t.Fatal("missing Hangzhou OSS endpoint")
	}
}
