package syncwire

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Both policy representations must preserve the legacy single schedule and the
// multi-schedule form through their generated wire adapters.
func TestAfterhoursWireRoundTrip(t *testing.T) {
	for _, raw := range []string{`"night"`, `["night","weekend"]`} {
		t.Run(raw, func(t *testing.T) {
			var policy Policy_Extensions_Afterhours
			var values PolicyValues_Extensions_Afterhours
			if err := json.Unmarshal([]byte(raw), &policy); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(raw), &values); err != nil {
				t.Fatal(err)
			}
			if raw[0] == '"' {
				a, err := policy.AsPolicyExtensionsAfterhours0()
				if err != nil || a != "night" {
					t.Fatalf("policy: %q %v", a, err)
				}
				b, err := values.AsPolicyValuesExtensionsAfterhours0()
				if err != nil || b != a {
					t.Fatalf("values: %q %v", b, err)
				}
				if _, err := policy.AsPolicyExtensionsAfterhours1(); err == nil {
					t.Fatal("single schedule decoded as list")
				}
				if _, err := values.AsPolicyValuesExtensionsAfterhours1(); err == nil {
					t.Fatal("single value decoded as list")
				}
				if err := policy.FromPolicyExtensionsAfterhours0(a); err != nil {
					t.Fatal(err)
				}
				if err := values.FromPolicyValuesExtensionsAfterhours0(b); err != nil {
					t.Fatal(err)
				}
			} else {
				want := []string{"night", "weekend"}
				a, err := policy.AsPolicyExtensionsAfterhours1()
				if err != nil || !reflect.DeepEqual(a, want) {
					t.Fatalf("policy: %v %v", a, err)
				}
				b, err := values.AsPolicyValuesExtensionsAfterhours1()
				if err != nil || !reflect.DeepEqual(b, want) {
					t.Fatalf("values: %v %v", b, err)
				}
				if _, err := policy.AsPolicyExtensionsAfterhours0(); err == nil {
					t.Fatal("list decoded as single schedule")
				}
				if _, err := values.AsPolicyValuesExtensionsAfterhours0(); err == nil {
					t.Fatal("list decoded as single value")
				}
				if err := policy.FromPolicyExtensionsAfterhours1(a); err != nil {
					t.Fatal(err)
				}
				if err := values.FromPolicyValuesExtensionsAfterhours1(b); err != nil {
					t.Fatal(err)
				}
			}
			for _, value := range []any{policy, values} {
				got, err := json.Marshal(value)
				if err != nil || string(got) != raw {
					t.Fatalf("round trip = %s, %v; want %s", got, err, raw)
				}
			}
		})
	}
}

func TestAfterhoursClosedBoundary(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{`"night"`, true}, {`["night","weekend"]`, true},
		{`[]`, false}, {`["night"]`, false}, {`null`, false},
		{`42`, false}, {`["night",42]`, false}, {`{"id":"night"}`, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var target PolicyValues
			body := []byte(`{"extensions":[{"label":"Office","pinSet":true,"handsets":["desk"],"afterhours":` + tc.raw + `}]}`)
			err := Decode("PolicyValues", body, &target)
			if (err == nil) != tc.valid {
				t.Fatalf("Decode(%s) = %v, valid=%v", tc.raw, err, tc.valid)
			}
		})
	}
}
