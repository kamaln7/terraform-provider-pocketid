package resources

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
)

func TestOptionalAttribute(t *testing.T) {
	tests := []struct {
		name  string
		prior types.String
		value string
		want  types.String
	}{
		{"a value is kept", types.StringNull(), "Kamal", types.StringValue("Kamal")},
		{"a value replaces the prior one", types.StringValue("old"), "new", types.StringValue("new")},
		{"empty is null when unset", types.StringNull(), "", types.StringNull()},
		{"empty is null when the prior value was unknown", types.StringUnknown(), "", types.StringNull()},
		{"empty is null when the value was removed", types.StringValue("old"), "", types.StringNull()},
		{"an empty prior value stays empty", types.StringValue(""), "", types.StringValue("")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, optionalAttribute(tt.prior, tt.value))
		})
	}
}
