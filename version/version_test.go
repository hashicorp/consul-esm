package version

import (
	"testing"
)

func TestCheckConsulVersions(t *testing.T) {
	testCases := []struct {
		name     string
		versions []string
		err      bool
	}{
		{
			"nil",
			nil,
			true,
		}, {
			"empty",
			[]string{},
			true,
		}, {
			"valid one",
			[]string{"1.4.1"},
			false,
		}, {
			"valid",
			[]string{"1.4.2-dev", "1.5.0+ent", "1.8.0"},
			false,
		}, {
			"invalid one",
			[]string{"1.4.0"},
			true,
		}, {
			"invalid",
			[]string{"1.8.1", "1.3.1", "1.4.0-dev"},
			true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckConsulVersions(tc.versions)
			if tc.err && err == nil {
				t.Logf("expected an error for version(s): %q", tc.versions)
				t.Fail()
			} else if !tc.err && err != nil {
				t.Logf("unexpected error for versions %q: %s", tc.versions, err)
				t.Fail()
			}
		})
	}
}
