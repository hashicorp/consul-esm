package main

import (
	"io/ioutil"
	"log"
	"os"
	"testing"

	"github.com/hashicorp/consul/testutil"
)

// Change as needed to see log/consul-agent output
var output = ioutil.Discard
var LOGOUT = output
var STDOUT = output
var STDERR = output

func TestMain(m *testing.M) {
	log.SetOutput(LOGOUT)
	os.Exit(m.Run())
}

func NewTestServer() (*testutil.TestServer, error) {
	return testutil.NewTestServerConfig(func(c *testutil.TestServerConfig) {
		c.Stdout = STDOUT
		c.Stderr = STDERR
	})
}
