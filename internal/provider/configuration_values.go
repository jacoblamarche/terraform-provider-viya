package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// This file converts between the dynamically typed `properties` attribute of
// viya_configuration and the JSON values of the Viya Configuration service.
//
// On the Terraform side, a property value is a string, a number or a bool.
// On the Viya side, it is decoded from JSON as a string, a json.Number or a
// bool.

// propertyValues returns the attributes of a `properties` value, which is an
// object or a map. It returns false when the value is neither, or is null
// or unknown.
func propertyValues(value types.Dynamic) (map[string]attr.Value, bool) {
	if value.IsNull() || value.IsUnknown() || value.IsUnderlyingValueNull() || value.IsUnderlyingValueUnknown() {
		return nil, false
	}
	switch underlying := value.UnderlyingValue().(type) {
	case basetypes.ObjectValue:
		return underlying.Attributes(), true
	case basetypes.MapValue:
		return underlying.Elements(), true
	default:
		return nil, false
	}
}

// propertyToAPI converts one Terraform property value to its JSON form. It
// returns false for a value that is not a known string, number or bool.
func propertyToAPI(value attr.Value) (any, bool) {
	if value.IsNull() || value.IsUnknown() {
		return nil, false
	}
	switch typed := value.(type) {
	case basetypes.StringValue:
		return typed.ValueString(), true
	case basetypes.BoolValue:
		return typed.ValueBool(), true
	case basetypes.NumberValue:
		return json.Number(typed.ValueBigFloat().Text('f', -1)), true
	default:
		return nil, false
	}
}

// propertyFromAPI converts a JSON value read from Viya to a Terraform value.
//
// When prior is the value that Terraform already holds for the property, the
// result takes its type whenever the JSON value can be represented in it.
// This keeps `port = "636"` from showing as a change when Viya answers 636:
// the two are the same setting written differently.
func propertyFromAPI(value any, prior attr.Value) attr.Value {
	switch prior.(type) {
	case basetypes.StringValue:
		switch typed := value.(type) {
		case string:
			return types.StringValue(typed)
		case json.Number:
			return types.StringValue(typed.String())
		case bool:
			return types.StringValue(strconv.FormatBool(typed))
		}
	case basetypes.NumberValue:
		switch typed := value.(type) {
		case json.Number:
			if number, ok := parseNumber(typed.String()); ok {
				return number
			}
		case string:
			if number, ok := parseNumber(typed); ok {
				return number
			}
		}
	case basetypes.BoolValue:
		switch typed := value.(type) {
		case bool:
			return types.BoolValue(typed)
		case string:
			if parsed, err := strconv.ParseBool(typed); err == nil {
				return types.BoolValue(parsed)
			}
		}
	}
	return naturalPropertyValue(value)
}

// naturalPropertyValue converts a JSON value to the Terraform type that
// matches its JSON type. Arrays and objects, which the resource does not
// model, are rendered as their JSON text.
func naturalPropertyValue(value any) attr.Value {
	switch typed := value.(type) {
	case string:
		return types.StringValue(typed)
	case bool:
		return types.BoolValue(typed)
	case json.Number:
		if number, ok := parseNumber(typed.String()); ok {
			return number
		}
		return types.StringValue(typed.String())
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return types.StringValue(fmt.Sprint(value))
		}
		return types.StringValue(string(encoded))
	}
}

func parseNumber(text string) (types.Number, bool) {
	// 512 bits of precision is what Terraform itself uses for numbers.
	number, _, err := big.ParseFloat(text, 10, 512, big.ToNearestEven)
	if err != nil {
		return types.NumberNull(), false
	}
	return types.NumberValue(number), true
}

// propertiesValue builds a `properties` value from converted attributes.
//
// It keeps the shape that Terraform already holds: if prior is a map and
// every value still has the element type of that map, the result is a map;
// otherwise it is an object. Changing the shape needlessly would show as a
// difference in the plan.
func propertiesValue(ctx context.Context, values map[string]attr.Value, prior types.Dynamic) types.Dynamic {
	if !prior.IsNull() && !prior.IsUnknown() {
		if priorMap, ok := prior.UnderlyingValue().(basetypes.MapValue); ok {
			elementType := priorMap.ElementType(ctx)
			uniform := true
			for _, value := range values {
				if !value.Type(ctx).Equal(elementType) {
					uniform = false
					break
				}
			}
			if uniform {
				return types.DynamicValue(types.MapValueMust(elementType, values))
			}
		}
	}

	attributeTypes := make(map[string]attr.Type, len(values))
	for name, value := range values {
		attributeTypes[name] = value.Type(ctx)
	}
	return types.DynamicValue(types.ObjectValueMust(attributeTypes, values))
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
