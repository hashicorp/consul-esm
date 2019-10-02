package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/sdk/testutil"
)

// Change as needed to see log/consul-agent output
var output = ioutil.Discard
var LOGOUT = output
var STDOUT = output
var STDERR = output

func TestMain(m *testing.M) {
	log.SetOutput(LOGOUT)

	MaxRTT = 500 * time.Millisecond
	retryTime = 200 * time.Millisecond
	agentTTL = 100 * time.Millisecond

	os.Exit(m.Run())
}

//////
// utility functions used in tests

// New consul test process
func NewTestServer() (*testutil.TestServer, error) {
	return testutil.NewTestServerConfig(func(c *testutil.TestServerConfig) {
		c.Stdout = STDOUT
		c.Stderr = STDERR
	})
}

// Show diff between structs
func structDiff(act, exp interface{}) string {
	var b strings.Builder
	b.WriteString("\n")
	va := valueOf(act)
	ve := valueOf(exp)
	te := ve.Type()

	for i := 0; i < ve.NumField(); i++ {
		if !ve.Field(i).CanInterface() {
			continue
		}
		fe := ve.Field(i).Interface()
		fa := va.Field(i).Interface()
		if !reflect.DeepEqual(fe, fa) {
			fmt.Fprintf(&b, "%s: ", te.Field(i).Name)
			fmt.Fprintf(&b, "got: %#v", fa)
			fmt.Fprintf(&b, ", want: %#v", fe)
		}
	}

	return b.String()
}

// Compare 2 api.HealthChecks objects for important differences
func compareHealthCheck(c1, c2 *api.HealthCheck) error {
	var skip = map[string]bool{
		"CreateIndex": true,
		"ModifyIndex": true,
		"ServiceTags": true,
	}
	errs := []string{}
	valof := valueOf(c1)
	for i := 0; i < valof.NumField(); i++ {
		f := valof.Type().Field(i).Name
		if skip[f] {
			continue
		}
		if err := compareFields(c1, c2, f); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("Checks differ: %s", strings.Join(errs, ", "))
	}
	return nil
}

func compareFields(act, exp interface{}, field string) error {
	av, ev := valueOf(act), valueOf(exp)
	af := av.FieldByName(field).Interface()
	ef := ev.FieldByName(field).Interface()
	if !reflect.DeepEqual(af, ef) {
		return fmt.Errorf("%s(got: %v, want: %v)", field, af, ef)
	}
	return nil
}

func valueOf(o interface{}) reflect.Value {
	if reflect.TypeOf(o).Kind() == reflect.Ptr {
		return reflect.ValueOf(o).Elem()
	}
	return reflect.ValueOf(o)
}
