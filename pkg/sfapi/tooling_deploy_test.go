package sfapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ko4edikov/sff/pkg/auth"
)

// toolingServer stands in for the org, driving ToolingDeploy through its flow.
// finalState is the ContainerAsyncRequest State returned by the status poll.
func toolingServer(t *testing.T, finalState, failBody string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(p, "tooling/query"):
			_, _ = w.Write([]byte(`{"totalSize":1,"done":true,"records":[{"Id":"01pxx0000000001","Name":"Foo"}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/MetadataContainer"):
			_, _ = w.Write([]byte(`{"id":"0Mxxx","success":true}`))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/ApexClassMember"):
			_, _ = w.Write([]byte(`{"id":"0Vxxx","success":true}`))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/ContainerAsyncRequest"):
			_, _ = w.Write([]byte(`{"id":"1drxx","success":true}`))
		case r.Method == http.MethodGet && strings.Contains(p, "ContainerAsyncRequest/"):
			if failBody != "" {
				_, _ = w.Write([]byte(failBody))
				return
			}
			_, _ = w.Write([]byte(`{"State":"` + finalState + `"}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, p)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	return &Client{
		Org:        &auth.Org{InstanceURL: srv.URL, AccessToken: "tok", Username: "me@test"},
		APIVersion: "v60.0",
		HTTP:       srv.Client(),
	}
}

func TestToolingDeploy_Success(t *testing.T) {
	c := toolingServer(t, "Completed", "")
	comps := []ToolingComponent{{Type: "ApexClass", Name: "Foo", Body: "public class Foo {}"}}

	res, err := c.ToolingDeploy(context.Background(), ToolingDeployInput{Apex: comps}, false, nil)
	if err != nil {
		t.Fatalf("ToolingDeploy: %v", err)
	}
	if len(res.Succeeded) != 1 || res.Succeeded[0] != "ApexClass:Foo" {
		t.Errorf("Succeeded = %v", res.Succeeded)
	}
	if res.CheckOnly {
		t.Errorf("CheckOnly should be false")
	}
}

func TestToolingDeploy_CompileFailure(t *testing.T) {
	fail := `{"State":"Failed","ErrorMsg":"compile error","DeployDetails":{"componentFailures":[` +
		`{"fullName":"Foo","problem":"Unexpected token","lineNumber":3,"columnNumber":5,"success":false}]}}`
	c := toolingServer(t, "", fail)
	comps := []ToolingComponent{{Type: "ApexClass", Name: "Foo", Body: "public class Foo {"}}

	res, err := c.ToolingDeploy(context.Background(), ToolingDeployInput{Apex: comps}, false, nil)
	if err == nil {
		t.Fatal("want error for failed deploy, got nil")
	}
	if len(res.Errors) != 1 {
		t.Fatalf("Errors = %v", res.Errors)
	}
	e := res.Errors[0]
	if e.Component != "Foo" || e.Line != 3 || !strings.Contains(e.Problem, "Unexpected token") {
		t.Errorf("error = %+v", e)
	}
}

func TestToolingDeploy_MissingEntity(t *testing.T) {
	// Query returns Foo's id, but we ask to deploy Bar, which is absent.
	c := toolingServer(t, "Completed", "")
	comps := []ToolingComponent{{Type: "ApexClass", Name: "Bar", Body: "public class Bar {}"}}

	_, err := c.ToolingDeploy(context.Background(), ToolingDeployInput{Apex: comps}, false, nil)
	if err == nil || !strings.Contains(err.Error(), "not present in org") {
		t.Fatalf("want missing-entity error, got %v", err)
	}
}

func TestToolingDeploy_StaticResources(t *testing.T) {
	var posted, patched bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(p, "tooling/query"):
			// "New" is absent; "Old" already exists with an id.
			_, _ = w.Write([]byte(`{"totalSize":1,"done":true,"records":[{"Id":"081xx","Name":"Old"}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/StaticResource"):
			posted = true
			_, _ = w.Write([]byte(`{"id":"081yy","success":true}`))
		case r.Method == http.MethodPatch && strings.Contains(p, "/StaticResource/081xx"):
			patched = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, p)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{
		Org:        &auth.Org{InstanceURL: srv.URL, AccessToken: "tok", Username: "me@test"},
		APIVersion: "v60.0",
		HTTP:       srv.Client(),
	}

	in := ToolingDeployInput{Static: []ToolingStaticResource{
		{Name: "New", ContentType: "text/plain", Body: []byte("hi")},
		{Name: "Old", ContentType: "text/plain", Body: []byte("bye")},
	}}
	res, err := c.ToolingDeploy(context.Background(), in, false, nil)
	if err != nil {
		t.Fatalf("ToolingDeploy: %v", err)
	}
	if !posted {
		t.Error("absent resource should have been created (POST)")
	}
	if !patched {
		t.Error("existing resource should have been updated (PATCH)")
	}
	if len(res.Succeeded) != 2 {
		t.Errorf("Succeeded = %v", res.Succeeded)
	}
}

func TestToolingDeploy_Aura(t *testing.T) {
	var posted, patched int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, q := r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(p, "tooling/query") && strings.Contains(q, "DefType"):
			// COMPONENT already exists; CONTROLLER is new.
			_, _ = w.Write([]byte(`{"totalSize":1,"done":true,"records":[{"Id":"1ttxx","DefType":"COMPONENT"}]}`))
		case strings.Contains(p, "tooling/query") && strings.Contains(q, "AuraDefinitionBundle"):
			_, _ = w.Write([]byte(`{"totalSize":1,"done":true,"records":[{"Id":"0Abxx"}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/AuraDefinition"):
			posted++
			_, _ = w.Write([]byte(`{"id":"1ttyy","success":true}`))
		case r.Method == http.MethodPatch && strings.Contains(p, "/AuraDefinition/1ttxx"):
			patched++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s?%s", r.Method, p, q)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{
		Org:        &auth.Org{InstanceURL: srv.URL, AccessToken: "tok", Username: "me@test"},
		APIVersion: "v60.0",
		HTTP:       srv.Client(),
	}

	in := ToolingDeployInput{Aura: []ToolingAuraBundle{{Name: "myCmp", Files: []AuraFile{
		{DefType: "COMPONENT", Format: "XML", Source: "<aura:component/>"},
		{DefType: "CONTROLLER", Format: "JS", Source: "({})"},
	}}}}
	res, err := c.ToolingDeploy(context.Background(), in, false, nil)
	if err != nil {
		t.Fatalf("ToolingDeploy: %v", err)
	}
	if patched != 1 || posted != 1 {
		t.Errorf("patched=%d posted=%d, want 1/1", patched, posted)
	}
	if len(res.Succeeded) != 1 || res.Succeeded[0] != "AuraDefinitionBundle:myCmp" {
		t.Errorf("Succeeded = %v", res.Succeeded)
	}
}

func TestToolingDeploy_AuraMissingBundle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Bundle lookup returns nothing.
		_, _ = w.Write([]byte(`{"totalSize":0,"done":true,"records":[]}`))
	}))
	t.Cleanup(srv.Close)
	c := &Client{
		Org:        &auth.Org{InstanceURL: srv.URL, AccessToken: "tok", Username: "me@test"},
		APIVersion: "v60.0",
		HTTP:       srv.Client(),
	}

	in := ToolingDeployInput{Aura: []ToolingAuraBundle{{Name: "ghost", Files: []AuraFile{{DefType: "COMPONENT", Format: "XML", Source: "x"}}}}}
	res, err := c.ToolingDeploy(context.Background(), in, false, nil)
	if err == nil {
		t.Fatal("want error for missing bundle, got nil")
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Problem, "not present in org") {
		t.Errorf("Errors = %+v", res.Errors)
	}
}

func TestToolingDeploy_Lwc(t *testing.T) {
	var posted, patched int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, q := r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(p, "tooling/query") && strings.Contains(q, "FilePath"):
			// myCmp.js already exists; myCmp.html is new.
			_, _ = w.Write([]byte(`{"totalSize":1,"done":true,"records":[{"Id":"0Rrxx","FilePath":"lwc/myCmp/myCmp.js"}]}`))
		case strings.Contains(p, "tooling/query") && strings.Contains(q, "LightningComponentBundle"):
			_, _ = w.Write([]byte(`{"totalSize":1,"done":true,"records":[{"Id":"0Roxx"}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/LightningComponentResource"):
			posted++
			_, _ = w.Write([]byte(`{"id":"0Rryy","success":true}`))
		case r.Method == http.MethodPatch && strings.Contains(p, "/LightningComponentResource/0Rrxx"):
			patched++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s?%s", r.Method, p, q)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{
		Org:        &auth.Org{InstanceURL: srv.URL, AccessToken: "tok", Username: "me@test"},
		APIVersion: "v60.0",
		HTTP:       srv.Client(),
	}

	in := ToolingDeployInput{Lwc: []ToolingLwcBundle{{Name: "myCmp", Files: []LwcFile{
		{FilePath: "lwc/myCmp/myCmp.js", Format: "js", Source: "export default {}"},
		{FilePath: "lwc/myCmp/myCmp.html", Format: "html", Source: "<template></template>"},
	}}}}
	res, err := c.ToolingDeploy(context.Background(), in, false, nil)
	if err != nil {
		t.Fatalf("ToolingDeploy: %v", err)
	}
	if patched != 1 || posted != 1 {
		t.Errorf("patched=%d posted=%d, want 1/1", patched, posted)
	}
	if len(res.Succeeded) != 1 || res.Succeeded[0] != "LightningComponentBundle:myCmp" {
		t.Errorf("Succeeded = %v", res.Succeeded)
	}
}

func TestToolingError(t *testing.T) {
	body := []byte(`[{"message":"Invalid type: Foo","errorCode":"INVALID_TYPE"}]`)
	err := toolingError(http.StatusBadRequest, body)
	if !strings.Contains(err.Error(), "Invalid type: Foo") {
		t.Errorf("toolingError = %v", err)
	}
}
