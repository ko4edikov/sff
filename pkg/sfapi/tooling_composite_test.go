package sfapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ko4edikov/sff/pkg/auth"
)

func TestToolingComposite_TooMany(t *testing.T) {
	c := &Client{Org: &auth.Org{InstanceURL: "http://x"}, APIVersion: "v60.0", HTTP: http.DefaultClient}
	subs := make([]compositeSub, MaxCompositeSubrequests+1)
	if _, err := c.toolingComposite(context.Background(), true, subs); err == nil ||
		!strings.Contains(err.Error(), "at most") {
		t.Fatalf("want too-many error, got %v", err)
	}
}

func TestCompositeHelpers(t *testing.T) {
	results := []compositeResult{
		{ReferenceID: "container", HTTPStatusCode: 201, Body: []byte(`{"id":"0Mxx","success":true}`)},
		{ReferenceID: "request", HTTPStatusCode: 201, Body: []byte(`{"id":"1drxx","success":true}`)},
	}
	ids := compositeIDs(results)
	if ids["container"] != "0Mxx" || ids["request"] != "1drxx" {
		t.Errorf("compositeIDs = %v", ids)
	}
	if err := compositeFirstError(results); err != nil {
		t.Errorf("compositeFirstError on all-success = %v", err)
	}

	results[1] = compositeResult{ReferenceID: "request", HTTPStatusCode: 400,
		Body: []byte(`[{"message":"bad request","errorCode":"X"}]`)}
	if err := compositeFirstError(results); err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Errorf("compositeFirstError on failure = %v", err)
	}
}

// quotedNames pulls the values out of a "... IN ('a','b')" SOQL fragment.
func quotedNames(q string) []string {
	var out []string
	for {
		i := strings.Index(q, "'")
		if i < 0 {
			break
		}
		q = q[i+1:]
		j := strings.Index(q, "'")
		if j < 0 {
			break
		}
		out = append(out, q[:j])
		q = q[j+1:]
	}
	return out
}

// TestToolingDeploy_ApexChunked drives the fallback path taken when the
// container, its members, and the request can't fit one composite call: the
// container is created alone, members are chunked by MaxCompositeSubrequests,
// and the request is submitted last.
func TestToolingDeploy_ApexChunked(t *testing.T) {
	const n = 30 // > MaxCompositeSubrequests-2, and spans two member chunks (25 + 5)
	var containerCreates, requestCreates, compositeCalls, memberSubs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(p, "tooling/query"):
			recs := make([]string, 0, n)
			for _, name := range quotedNames(r.URL.Query().Get("q")) {
				recs = append(recs, `{"Id":"`+name+`-id","Name":"`+name+`"}`)
			}
			_, _ = w.Write([]byte(`{"totalSize":1,"done":true,"records":[` + strings.Join(recs, ",") + `]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/MetadataContainer"):
			containerCreates++
			_, _ = w.Write([]byte(`{"id":"0Mxxx","success":true}`))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "tooling/composite"):
			compositeCalls++
			subs := decodeComposite(t, r)
			memberSubs += len(subs)
			writeComposite(w, subs)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/ContainerAsyncRequest"):
			requestCreates++
			_, _ = w.Write([]byte(`{"id":"1drxx","success":true}`))
		case r.Method == http.MethodGet && strings.Contains(p, "ContainerAsyncRequest/"):
			_, _ = w.Write([]byte(`{"State":"Completed"}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, p)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{Org: &auth.Org{InstanceURL: srv.URL, AccessToken: "tok"}, APIVersion: "v60.0", HTTP: srv.Client()}

	comps := make([]ToolingComponent, n)
	for i := range comps {
		comps[i] = ToolingComponent{Type: "ApexClass", Name: fmt.Sprintf("C%d", i), Body: "x"}
	}
	res, err := c.ToolingDeploy(context.Background(), ToolingDeployInput{Apex: comps}, false, nil)
	if err != nil {
		t.Fatalf("ToolingDeploy: %v", err)
	}
	if len(res.Succeeded) != n {
		t.Errorf("Succeeded = %d, want %d", len(res.Succeeded), n)
	}
	if containerCreates != 1 || requestCreates != 1 {
		t.Errorf("containerCreates=%d requestCreates=%d, want 1/1", containerCreates, requestCreates)
	}
	if compositeCalls != 2 || memberSubs != n {
		t.Errorf("compositeCalls=%d memberSubs=%d, want 2/%d", compositeCalls, memberSubs, n)
	}
}

// TestToolingDeploy_StaticResourcePartialError confirms allOrNone=false upserts
// report per-resource failures without hiding the resources that succeeded.
func TestToolingDeploy_StaticResourcePartialError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(p, "tooling/query"):
			_, _ = w.Write([]byte(`{"totalSize":1,"done":true,"records":[{"Id":"081xx","Name":"Old"}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "tooling/composite"):
			subs := decodeComposite(t, r)
			parts := make([]string, len(subs))
			for i, s := range subs {
				if s.Method == http.MethodPost { // creating "New" fails
					parts[i] = `{"referenceId":"` + s.ReferenceID + `","httpStatusCode":400,` +
						`"body":[{"message":"insufficient access","errorCode":"X"}]}`
					continue
				}
				parts[i] = `{"referenceId":"` + s.ReferenceID + `","httpStatusCode":204,"body":null}`
			}
			_, _ = w.Write([]byte(`{"compositeResponse":[` + strings.Join(parts, ",") + `]}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, p)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{Org: &auth.Org{InstanceURL: srv.URL, AccessToken: "tok"}, APIVersion: "v60.0", HTTP: srv.Client()}

	in := ToolingDeployInput{Static: []ToolingStaticResource{
		{Name: "New", ContentType: "text/plain", Body: []byte("hi")},
		{Name: "Old", ContentType: "text/plain", Body: []byte("bye")},
	}}
	res, err := c.ToolingDeploy(context.Background(), in, false, nil)
	if err == nil {
		t.Fatal("want error for partial failure, got nil")
	}
	if len(res.Succeeded) != 1 || res.Succeeded[0] != "StaticResource:Old" {
		t.Errorf("Succeeded = %v", res.Succeeded)
	}
	if len(res.Errors) != 1 || res.Errors[0].Component != "StaticResource:New" ||
		!strings.Contains(res.Errors[0].Problem, "insufficient access") {
		t.Errorf("Errors = %+v", res.Errors)
	}
}
