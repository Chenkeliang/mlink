package identity

import (
	"fmt"
	"strings"
	"testing"
)

func TestBindingValueFormattingRedactsRawValue(t *testing.T) {
	binding := BindingValue{RefID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", Value: []byte("on_actual_secret"), PrincipalID: "owner"}
	for _, rendered := range []string{fmt.Sprint(binding), fmt.Sprintf("%#v", binding)} {
		if strings.Contains(rendered, "on_actual_secret") || !strings.Contains(rendered, "owner-feishu-union-1") {
			t.Fatalf("rendered binding = %q", rendered)
		}
	}
}
