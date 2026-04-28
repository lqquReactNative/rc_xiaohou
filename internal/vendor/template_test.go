package vendor_test

import (
	"testing"

	"github.com/lqquReactNative/rc_xiaohou/internal/vendor"
)

// AC2: template with {{user_id}} renders correct value from payload.
func TestRenderTemplate(t *testing.T) {
	t.Run("substitutes known variables", func(t *testing.T) {
		tmpl := `{"user_id":"{{user_id}}","event":"{{event}}"}`
		payload := map[string]interface{}{
			"user_id": "u-42",
			"event":   "purchase",
		}
		got, err := vendor.RenderTemplate(tmpl, payload)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := `{"user_id":"u-42","event":"purchase"}`
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("returns error for missing variable", func(t *testing.T) {
		tmpl := `{"user_id":"{{user_id}}"}`
		payload := map[string]interface{}{}
		_, err := vendor.RenderTemplate(tmpl, payload)
		if err == nil {
			t.Fatal("expected error for missing variable, got nil")
		}
	})

	t.Run("template with no placeholders returns unchanged", func(t *testing.T) {
		tmpl := `{"static":"value"}`
		got, err := vendor.RenderTemplate(tmpl, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != tmpl {
			t.Errorf("got %q, want %q", got, tmpl)
		}
	})
}
