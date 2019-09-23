package main

import (
	"io/ioutil"
	"log"
	"os"
	"testing"

	"github.com/hashicorp/consul/testutil"
)

// Change as needed to see log/consul-agent output
var LOGOUT = ioutil.Discard
var STDOUT = ioutil.Discard
var STDERR = ioutil.Discard

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
