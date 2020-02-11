package span

import (
	"testing"
	"time"
)

func TestJSONUnmarshal(t *testing.T) {
	b := []byte(`{"id":"hello","name":"bowser","timestamp":1480979203000000,"binaryAnnotations":[{"key":"hello","value":"world"}],"duration":1000}`)
	span := new(Span)
	err := span.UnmarshalJSON(b)
	if err != nil {
		t.Errorf("Failed to unmarshal span from json data %s: %v", string(b), err)
	}
	if span.Duration != 1000*time.Microsecond {
		t.Errorf("span duration was incorrectly parsed")
	}
	entry := span.BinaryAnnotations[0]
	if entry.Value != "world" || entry.Key != "hello" {
		t.Errorf("binary annotations incorrectly parsed")
	}
}
