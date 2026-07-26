package applicationjob

import (
	"context"
	"strings"
	"testing"
)

func TestHostHandlerRejectsNonPublicRootFields(t *testing.T) {
	handler := NewHandler(nil, "caller")
	_, err := handler(context.Background(), map[string]any{
		"op": "submit", "template": "publish", "input": map[string]any{},
		"session_id": "private",
	})
	if err == nil || !strings.Contains(err.Error(), `unknown input key "session_id"`) {
		t.Fatalf("handler error = %v", err)
	}
}

func TestHostHandlerPublicShapes(t *testing.T) {
	for _, test := range []struct {
		op   string
		args map[string]any
		want string
	}{
		{op: "submit", args: map[string]any{"op": "submit", "input": map[string]any{}}, want: "template is required"},
		{op: "status", args: map[string]any{"op": "status"}, want: "job_ref is required"},
		{op: "cancel", args: map[string]any{"op": "cancel"}, want: "job_ref is required"},
	} {
		t.Run(test.op, func(t *testing.T) {
			handler := NewHandler(nil, "caller")
			_, err := handler(context.Background(), test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("handler error = %v, want %q", err, test.want)
			}
		})
	}
}
