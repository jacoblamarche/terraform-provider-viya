package provider

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func number(value float64) types.Number {
	return types.NumberValue(big.NewFloat(value))
}

func TestPropertyFromAPI(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		value any
		prior attr.Value
		want  attr.Value
	}{
		// The type that Terraform holds is kept when the value fits in it.
		"number read into a string": {json.Number("636"), types.StringValue("636"), types.StringValue("636")},
		"bool read into a string":   {true, types.StringValue("true"), types.StringValue("true")},
		"string read into a number": {"636", number(636), number(636)},
		"string read into a bool":   {"false", types.BoolValue(true), types.BoolValue(false)},
		"number read into a number": {json.Number("0.5"), number(1), number(0.5)},
		"string read into a string": {"ldap", types.StringValue("x"), types.StringValue("ldap")},
		"bool read into a bool":     {true, types.BoolValue(false), types.BoolValue(true)},
		// Otherwise the value takes its own type, which shows as a change.
		"text read into a number":         {"many", number(1), types.StringValue("many")},
		"text read into a bool":           {"maybe", types.BoolValue(true), types.StringValue("maybe")},
		"number read into a bool":         {json.Number("1"), types.BoolValue(true), number(1)},
		"no prior value":                  {json.Number("5"), nil, number(5)},
		"array rendered as its JSON text": {[]any{"a", "b"}, nil, types.StringValue(`["a","b"]`)},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := propertyFromAPI(tc.value, tc.prior); !got.Equal(tc.want) {
				t.Errorf("propertyFromAPI(%#v, %v) = %v, want %v", tc.value, tc.prior, got, tc.want)
			}
		})
	}
}

func TestPropertyToAPI(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		value  attr.Value
		want   any
		wantOK bool
	}{
		"string":         {types.StringValue("ldap"), "ldap", true},
		"bool":           {types.BoolValue(true), true, true},
		"integer":        {number(636), json.Number("636"), true},
		"fraction":       {number(0.5), json.Number("0.5"), true},
		"null":           {types.StringNull(), nil, false},
		"unknown":        {types.StringUnknown(), nil, false},
		"list of string": {types.ListValueMust(types.StringType, []attr.Value{types.StringValue("a")}), nil, false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, ok := propertyToAPI(tc.value)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("propertyToAPI(%v) = %#v, %t; want %#v, %t", tc.value, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestPropertiesValue_keepsTheShapeOfThePriorValue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	strings := map[string]attr.Value{"a": types.StringValue("1"), "b": types.StringValue("2")}
	mixed := map[string]attr.Value{"a": types.StringValue("1"), "b": types.BoolValue(true)}
	priorMap := types.DynamicValue(types.MapValueMust(types.StringType, map[string]attr.Value{"a": types.StringValue("0")}))

	if got := propertiesValue(ctx, strings, priorMap); !got.UnderlyingValue().Type(ctx).Equal(types.MapType{ElemType: types.StringType}) {
		t.Errorf("a map whose values kept their type became %s", got.UnderlyingValue().Type(ctx))
	}
	if _, isObject := propertiesValue(ctx, mixed, priorMap).UnderlyingValue().(types.Object); !isObject {
		t.Error("a map whose values no longer share a type must become an object")
	}
	if _, isObject := propertiesValue(ctx, strings, types.DynamicNull()).UnderlyingValue().(types.Object); !isObject {
		t.Error("without a prior value, properties must be an object")
	}
}
