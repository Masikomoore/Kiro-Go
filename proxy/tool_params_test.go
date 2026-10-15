package proxy

import (
	"bytes"
	"kiro-go/config"
	"net/http"
	"reflect"
	"testing"
)

func TestNormalizeToolUseParams(t *testing.T) {
	cases := []struct {
		name string
		in   KiroToolUse
		want map[string]interface{}
	}{
		{"write aliases", KiroToolUse{Name: "Write", Input: map[string]interface{}{"path": "/a", "text": "hi"}},
			map[string]interface{}{"file_path": "/a", "content": "hi"}},
		{"edit aliases", KiroToolUse{Name: "Edit", Input: map[string]interface{}{"path": "/a", "oldStr": "x", "newStr": "y"}},
			map[string]interface{}{"file_path": "/a", "old_string": "x", "new_string": "y"}},
		{"claude names win", KiroToolUse{Name: "Edit", Input: map[string]interface{}{"file_path": "/a", "path": "/b", "old_string": "x", "new_string": "y"}},
			map[string]interface{}{"file_path": "/a", "path": "/b", "old_string": "x", "new_string": "y"}},
		{"read line range", KiroToolUse{Name: "Read", Input: map[string]interface{}{"path": "/a", "start_line": float64(10), "end_line": float64(19)}},
			map[string]interface{}{"file_path": "/a", "offset": float64(10), "limit": float64(10)}},
		{"grep aliases", KiroToolUse{Name: "Grep", Input: map[string]interface{}{"query": "foo", "includePattern": "*.go"}},
			map[string]interface{}{"pattern": "foo", "glob": "*.go"}},
		{"other tool untouched", KiroToolUse{Name: "write_file", Input: map[string]interface{}{"path": "/a"}},
			map[string]interface{}{"path": "/a"}},
	}
	for _, tc := range cases {
		normalizeToolUseParams(&tc.in)
		if !reflect.DeepEqual(tc.in.Input, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, tc.in.Input, tc.want)
		}
	}
}

func TestCallKiroAPINormalizesToolParamsWhenEnabled(t *testing.T) {
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.UpdateCompatSettings(config.CompatSettings{NormalizeKiroToolParams: true}); err != nil {
		t.Fatalf("update compat: %v", err)
	}
	t.Cleanup(func() { _ = config.UpdateCompatSettings(config.CompatSettings{}) })

	installKiroRetryTestEndpoints(t)
	installKiroStreamTestClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body bytes.Buffer
		body.Write(awsEventStreamFrame(t, "toolUseEvent", map[string]interface{}{
			"toolUseId": "t1", "name": "Write", "input": `{"path":"/tmp/a","text":"hello"}`, "stop": true,
		}))
		body.Write(awsEventStreamFrame(t, "metadataEvent", map[string]interface{}{"stopReason": "tool_use"}))
		return kiroStreamTestResponse(&body), nil
	}))

	var got KiroToolUse
	err := CallKiroAPI(newKiroRetryTestOAuthAccount(), newKiroRetryTestPayload(), &KiroStreamCallback{
		OnToolUse: func(tu KiroToolUse) { got = tu },
	})
	if err != nil {
		t.Fatalf("CallKiroAPI: %v", err)
	}
	want := map[string]interface{}{"file_path": "/tmp/a", "content": "hello"}
	if got.Name != "Write" || !reflect.DeepEqual(got.Input, want) {
		t.Fatalf("tool use = %+v", got)
	}
}
