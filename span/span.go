package span

import (
	"encoding/json"

	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/apache/thrift/lib/go/thrift"
	"github.com/uber/jaeger/thrift-gen/zipkincore"
)

// Span represents the Zipkin V1 Span object. See
// https://github.com/openzipkin/zipkin-api/blob/master/zipkin-api.yaml
type Span struct {
	TraceID           string
	Name              string
	ID                string
	ParentID          string
	Annotations       []*Annotation
	BinaryAnnotations []BinaryAnnotation
	Debug             bool
	Timestamp         time.Time
	Duration          time.Duration
	TraceIDHigh       *int64
}

// V1Spans is the result of thrift decoding the spans input
type v1Spans struct {
	spans []v1Span `thrift:"spans,1"`
}

// v1Span is used as an intermediate step between encoding and the Span tyhpe
type v1Span struct {
	TraceID           string             `thrift:"trace_id,1" json:"traceId"`
	Name              string             `thrift:"name,3" json:"name"`
	ID                string             `thrift:"id,4" json:"id"`
	ParentID          string             `thrift:"parent_id,5" json:"parentId,omitempty"`
	Annotations       []*Annotation      `thrift:"annotations,6" json:"annotations"`
	Debug             bool               `thrift:"debug,9" json:"debug,omitempty"`
	TraceIDHigh       *int64             `thrift:"trace_id_high,12" json:"traceIdHigh,omitempty"`
	BinaryAnnotations []BinaryAnnotation `thrift:"binary_annotations,8" json:"binaryAnnotations"`
	Timestamp         int64              `thrift:"timestamp,10" json:"timestamp,omitempty"`
	Duration          int64              `thrift:"duration,11" json:"duration,omitempty"`
}

type Annotation struct {
	Timestamp int64     `thrift:"timestamp,1" json:"timestamp"`
	Value     string    `thrift:"value,2" json:"value"`
	Host      *Endpoint `thrift:"host,3" json:"endpoint,omitempty"`
}

type AnnotationType int64

type BinaryAnnotation struct {
	Key            string         `thrift:"key,1" json:"key"`
	Value          interface{}    `thrift:"value,2" json:"value"`
	AnnotationType AnnotationType `thrift:"annotation_type,3" json:"annotationType"`
	Host           *Endpoint      `thrift:"host,4" json:"endpoint,omitempty"`
}

type Endpoint struct {
	Ipv4        string `thrift:"ipv4,1" json:"ipv4"`
	Port        int16  `thrift:"port,2" json:"port"`
	ServiceName string `thrift:"service_name,3" json:"serviceName"`
	Ipv6        []byte `thrift:"ipv6,4" json:"ipv6,omitempty"`
}

func (s Span) String() string {
	return fmt.Sprintf("%#v", s)
}

func (ba BinaryAnnotation) String() string {
	switch ba.AnnotationType {
	case AnnotationType(zipkincore.AnnotationType_STRING):
		return fmt.Sprintf("BinaryAnnotation({Key:%s Value:%s Host:%#v})", ba.Key, string(ba.Value.(string)), ba.Host)
	default:
		return fmt.Sprintf("BinaryAnnotation({Key:%s Value:%v Host:%#v})", ba.Key, ba.Value, ba.Host)
	}
}

func convertEndpoint(ep *zipkincore.Endpoint) *Endpoint {
	result := new(Endpoint)
	result.Ipv4 = convertIPv4(ep.Ipv4)
	result.Port = ep.Port
	result.ServiceName = ep.ServiceName
	return result
}

func convertDuration(duration int64) time.Duration {
	return time.Duration(duration * 1e3)
}

func convertTimestamp(timestamp int64) time.Time {
	return time.Unix(timestamp/1e6, (timestamp%1e6)*1e3)
}

// Span converts a JSONSpan into Span after Unmarshalling
func (v1span v1Span) Span() *Span {
	span := &Span{
		TraceID:           v1span.TraceID,
		Name:              v1span.Name,
		ID:                v1span.ID,
		ParentID:          v1span.ParentID,
		Annotations:       v1span.Annotations,
		BinaryAnnotations: convertJSONAnnotations(v1span.BinaryAnnotations),
		Debug:             v1span.Debug,
		TraceIDHigh:       v1span.TraceIDHigh,
	}
	span.Duration = convertDuration(v1span.Duration)
	span.Timestamp = convertTimestamp(v1span.Timestamp)
	return span
}

func convertJSONAnnotations(ba []BinaryAnnotation) []BinaryAnnotation {
	result := make([]BinaryAnnotation, len(ba))
	for index, ann := range ba {
		result[index] = BinaryAnnotation(ann)
		switch ann.Value.(type) {
		case int:
			result[index].AnnotationType = AnnotationType(zipkincore.AnnotationType_I64)
		case string:
			result[index].AnnotationType = AnnotationType(zipkincore.AnnotationType_STRING)
		case bool:
			result[index].AnnotationType = AnnotationType(zipkincore.AnnotationType_BOOL)
		default:
			result[index].AnnotationType = AnnotationType(zipkincore.AnnotationType_STRING)
		}
	}
	return result
}

func (v1spans v1Spans) Spans() []*Span {
	result := make([]*Span, len(v1spans.spans))
	for index, span := range v1spans.spans {
		result[index] = span.Span()
	}
	return result
}

// NewJSONSpan converts a Span into JSONSpan suitable for Marshalling
func newV1Span(span Span) v1Span {
	timestamp := span.Timestamp.UnixNano() / 1e3
	duration := span.Duration.Microseconds()
	v1span := v1Span{
		TraceID:           span.TraceID,
		Name:              span.Name,
		ID:                span.ID,
		ParentID:          span.ParentID,
		Annotations:       span.Annotations,
		Debug:             span.Debug,
		TraceIDHigh:       span.TraceIDHigh,
		BinaryAnnotations: span.BinaryAnnotations,
		Timestamp:         timestamp,
		Duration:          duration,
	}
	return v1span
}

func (s Span) MarshalJSON() ([]byte, error) {
	return json.Marshal(newV1Span(s))
}

func (s *Span) UnmarshalJSON(data []byte) error {
	var v1span v1Span
	logrus.WithField("json", string(data)).Trace("Unmarshalling span from json")
	if err := json.Unmarshal(data, &v1span); err != nil {
		return err
	}
	*s = *v1span.Span()
	logrus.WithField("span", s).Trace("Unmarshalled span from json")
	return nil
}

// thrift code taken from honeycomb-opentracing-proxy
func convertThriftSpan(ts *zipkincore.Span) *Span {
	s := &Span{
		TraceID:     convertID(ts.TraceID),
		Name:        ts.Name,
		ID:          convertID(ts.ID),
		Debug:       ts.Debug,
		TraceIDHigh: ts.TraceIDHigh,
		Annotations: make([]*Annotation, len(ts.Annotations)),
	}
	if ts.ParentID != nil && *ts.ParentID != 0 {
		s.ParentID = convertID(*ts.ParentID)
	}
	for i, annotation := range ts.Annotations {
		s.Annotations[i] = &Annotation{Host: convertEndpoint(annotation.Host), Value: annotation.Value, Timestamp: annotation.Timestamp}
	}

	if ts.Duration != nil {
		s.Duration = convertDuration(*ts.Duration)
	}

	if ts.Timestamp != nil {
		s.Timestamp = convertTimestamp(*ts.Timestamp)
	} else {
		s.Timestamp = time.Now().UTC()
	}

	s.BinaryAnnotations = make([]BinaryAnnotation, len(ts.BinaryAnnotations))

	for index, ba := range ts.BinaryAnnotations {
		if ba.Key == "ca" || ba.Key == "sa" {
			// BinaryAnnotations with key "ca" (client addr) or "sa" (server addr)
			// are special: the endpoint value for those is the address of the
			// *remote* source or destination of an RPC, rather than the local
			// hostname. See
			// https://github.com/openzipkin/zipkin/blob/c7b341b9b421e7a57c/zipkin/src/main/java/zipkin/Endpoint.java#L35
			// So for those, we don't want to lift the endpoint into the span's
			// own hostIPv4/ServiceName/etc. fields. Simply skip those for now.
			continue
		}
		s.BinaryAnnotations[index] = BinaryAnnotation{Host: convertEndpoint(ba.Host), Key: ba.Key, Value: convertBinaryAnnotationValue(ba), AnnotationType: AnnotationType(ba.AnnotationType)}
	}
	return s
}

func convertID(id int64) string {
	return fmt.Sprintf("%016x", uint64(id))
}

func convertIPv4(ip int32) string {
	return net.IPv4(byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip)).String()
}

func convertBinaryAnnotationValue(ba *zipkincore.BinaryAnnotation) interface{} {
	switch ba.AnnotationType {
	case zipkincore.AnnotationType_BOOL:
		return bytes.Compare(ba.Value, []byte{0}) == 1
	case zipkincore.AnnotationType_BYTES:
		return ba.Value
	case zipkincore.AnnotationType_DOUBLE, zipkincore.AnnotationType_I16, zipkincore.AnnotationType_I32, zipkincore.AnnotationType_I64:
		var number interface{}
		binary.Read(bytes.NewReader(ba.Value), binary.BigEndian, number)
		return number
	case zipkincore.AnnotationType_STRING:
		return string(ba.Value)
	}

	return ba.Value
}

// DecodeThrift reads a list of encoded thrift spans from an io.Reader, and
// converts that list to a slice of Spans.
// The implementation is based on jaeger internals, but not exported there.
func DecodeThrift(data []byte) ([]*Span, error) {
	buffer := thrift.NewTMemoryBuffer()
	buffer.Write(data)

	transport := thrift.NewTBinaryProtocolTransport(buffer)
	_, size, err := transport.ReadListBegin() // Ignore the returned element type
	if err != nil {
		return nil, err
	}

	// We don't depend on the size returned by ReadListBegin to preallocate the array because it
	// sometimes returns a nil error on bad input and provides an unreasonably large int for size
	var spans []*Span
	for i := 0; i < size; i++ {
		zs := &zipkincore.Span{}
		if err = zs.Read(transport); err != nil {
			return nil, err
		}
		logrus.WithField("span", zs).Trace("Unmarshalled span from thrift")
		span := convertThriftSpan(zs)
		logrus.WithField("span", span).Trace("Converted span from zipkin form")
		spans = append(spans, span)
	}

	return spans, nil
}

// AddTag adds a binary annotation with a key/value pair
// to the span
func (s *Span) AddTag(key string, value interface{}) {
	tag := BinaryAnnotation{Key: key, Value: value}
	s.BinaryAnnotations = append(s.BinaryAnnotations, tag)
}
