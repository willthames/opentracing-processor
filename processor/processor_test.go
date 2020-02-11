package processor

import (
	"flag"
	"testing"
)

type DummyApp struct {
	App
	dummy string
}

func NewDummyApp() *DummyApp {
	dummyApp := new(DummyApp)
	dummyApp.BaseCLI()
	flag.Parse()
	return dummyApp
}

func TestProcessor(t *testing.T) {
	dummyApp := NewDummyApp()
	if dummyApp.port != 8080 || dummyApp.metricsPort != 10010 {
		t.Errorf("dummyApp not correctly set up: %#v", dummyApp)
	}
}
