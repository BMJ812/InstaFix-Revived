package handlers

import (
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"testing"
)

func TestLoggedOutGraphQLRequestUsesCurrentContract(t *testing.T) {
	t.Setenv("INSTAFIX_MOBILE_GRAPHQL_DOC_ID", "")
	t.Setenv("INSTAFIX_MOBILE_GRAPHQL_USER_AGENT", "")

	req, err := newLoggedOutGraphQLRequest("DbK4jO6Nh0K")
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "POST" || req.URL.String() != "https://www.instagram.com/graphql/query/" {
		t.Fatalf("unexpected request target: %s %s", req.Method, req.URL)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatal(err)
	}
	if got := form.Get("doc_id"); got != "27128499623469141" {
		t.Fatalf("doc_id = %q", got)
	}
	if form.Get("lsd") == "" || form.Get("lsd") != req.Header.Get("X-Fb-Lsd") {
		t.Fatal("LSD form value and header must be present and equal")
	}

	var variables map[string]any
	if err := json.Unmarshal([]byte(form.Get("variables")), &variables); err != nil {
		t.Fatal(err)
	}
	if variables["shortcode"] != "DbK4jO6Nh0K" {
		t.Fatalf("shortcode = %#v", variables["shortcode"])
	}
	flag := "__relay_internal__pv__PolarisAIGMMediaWebLabelEnabledrelayprovider"
	if value, ok := variables[flag]; !ok || value != false {
		t.Fatalf("required relay variable = %#v, present=%v", value, ok)
	}
	if req.Header.Get("X-Fb-Friendly-Name") != "PolarisPostRootQuery" {
		t.Fatalf("friendly name = %q", req.Header.Get("X-Fb-Friendly-Name"))
	}
	if !strings.HasPrefix(req.UserAgent(), "Mozilla/5.0") {
		t.Fatalf("user agent = %q", req.UserAgent())
	}
}
