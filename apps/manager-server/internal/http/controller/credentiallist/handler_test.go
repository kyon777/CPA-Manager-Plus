package credentiallist

import (
	"net/url"
	"testing"
)

func TestParseRequestUsesSnakeCasePaginationAndProxyProjection(t *testing.T) {
	request, err := parseRequest(url.Values{
		"page":              {"4"},
		"page_size":         {"25"},
		"include_proxy_url": {"1"},
		"search":            {" Alice "},
		"sort":              {"recent_desc"},
	})
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if request.Page != 4 || request.PageSize != 25 || !request.IncludeProxyURL || request.Search != " Alice " || request.Sort != "recent_desc" {
		t.Fatalf("request = %#v", request)
	}
}

func TestParseRequestRejectsOversizedPage(t *testing.T) {
	if _, err := parseRequest(url.Values{"page_size": {"3000"}}); err == nil {
		t.Fatal("expected oversized page_size to be rejected")
	}
}
