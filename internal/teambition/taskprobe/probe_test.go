package taskprobe

import (
	"encoding/json"
	"testing"
)

func TestParseRef(t *testing.T) {
	in, err := ParseRef("https://www.teambition.com/project/p123456/tasks/view/x", "https://www.teambition.com/project/p123456/tasks/view/x/task/t123456")
	if err != nil || in.ProjectID != "p123456" || in.TaskID != "t123456" {
		t.Fatalf("unexpected ref: %#v %v", in, err)
	}
}
func TestParseRefRejectsMismatch(t *testing.T) {
	if _, err := ParseRef("bad", "bad"); err == nil {
		t.Fatal("expected invalid IDs")
	}
}
func TestUnwrapAndPaging(t *testing.T) {
	inner := `{"code":200,"result":{"items":[1,2],"nextPageToken":"next"}}`
	raw, _ := json.Marshal(map[string]any{"result": map[string]any{"content": []map[string]string{{"text": inner}}}})
	u := unwrap(raw)
	if !hasPageToken(u) || nextPageToken(u) != "next" || countItems(u) != 2 {
		t.Fatalf("unexpected unwrap: %s", u)
	}
}
func TestRetryable(t *testing.T) {
	for _, status := range []int{429, 502, 503, 504, 0} {
		if !retryable(status) {
			t.Errorf("%d should retry", status)
		}
	}
	for _, status := range []int{401, 403, 404} {
		if retryable(status) {
			t.Errorf("%d should not retry", status)
		}
	}
}

func TestRawPathDiffersByPageToken(t *testing.T) {
	a := rawPath(t.TempDir(), "List", map[string]any{"pageToken": "a"})
	b := rawPath(t.TempDir(), "List", map[string]any{"pageToken": "b"})
	if a == b {
		t.Fatal("paginated responses must not overwrite each other")
	}
}

func TestSafeFilename(t *testing.T) {
	if got := safeFilename(`bad<>:"/\\|?*.txt. `); got != "bad__________.txt" {
		t.Fatalf("unexpected sanitized name %q", got)
	}
}
