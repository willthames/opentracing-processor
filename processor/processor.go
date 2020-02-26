package processor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"github.com/willthames/opentracing-processor/span"
)

// App is a base processor struct suitable for embedding in
// specific processors (or using on its own if no extra fields are required)
type App struct {
	port         int
	metricsPort  int
	server       *http.Server
	collectorURL string
	logLevel     string
	Forwarder    *Forwarder
	OutputLines  []string
	Receiver     SpanReceiver
}

// SpanReceiver is an interface that accepts spans
type SpanReceiver interface {
	ReceiveSpan(span *span.Span)
}

// BaseCLI adds standard command line flags common to all
// opentracing processors
func (a *App) BaseCLI() {
	flag.IntVar(&a.port, "port", 8080, "server port")
	flag.IntVar(&a.metricsPort, "metrics-port", 10010, "prometheus /metrics port")
	flag.StringVar(&a.collectorURL, "collector-url", "", "Host to forward traces. Not setting this will work as dry run")
	flag.StringVar(&a.logLevel, "log-level", "Info", "log level")
}

// handleSpans handles the /api/v1/spans POST endpoint. It decodes the request
// body and normalizes it to a slice of types.Span instances. The Sink
// handles that slice. The Mirror, if configured, takes the request body
// verbatim and sends it to another host.
func (a *App) handleSpans(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		logrus.WithError(err).Error("Error reading request body")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("error reading request"))
	}

	contentType := r.Header.Get("Content-Type")

	var spans []*span.Span
	switch contentType {
	case "application/json":
		logrus.Info("Receiving data in json format")
		switch r.URL.Path {
		case "/api/v1/spans":
			err = json.Unmarshal(data, &spans)
		case "/api/v2/spans":
			err = json.Unmarshal(data, &spans)
		default:
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("invalid version"))
			return
		}
	case "application/x-thrift":
		logrus.Debug("Receiving data in thrift format")
		switch r.URL.Path {
		case "/api/v1/spans":
			spans, err = span.DecodeThrift(data)
		case "/api/v2/spans":
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("thrift is not supported for v2 spans"))
		default:
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("invalid version"))
			return
		}
	default:
		logrus.WithField("contentType", contentType).Error("unknown content type")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("unknown content type"))
		return
	}
	if err != nil {
		logrus.WithError(err).WithField("type", contentType).Error("error unmarshaling spans")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("error unmarshaling span data"))
		return
	}

	w.WriteHeader(http.StatusAccepted)
	for _, span := range spans {
		a.Receiver.ReceiveSpan(span)
	}
}

// ungzipWrap wraps a handleFunc and transparently ungzips the body of the
// request if it is gzipped
func ungzipWrap(hf func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var newBody io.ReadCloser
		isGzipped := r.Header.Get("Content-Encoding")
		if isGzipped == "gzip" {
			buf := bytes.Buffer{}
			if _, err := io.Copy(&buf, r.Body); err != nil {
				logrus.WithError(err).Error("error allocating buffer for ungzipping")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("error allocating buffer for ungzipping"))
				return
			}
			var err error
			newBody, err = gzip.NewReader(&buf)
			if err != nil {
				logrus.WithError(err).Error("error ungzipping span data")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("error ungzipping span data"))
				return
			}
			r.Body = newBody
		}
		hf(w, r)
	}
}

func (a *App) start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/spans", ungzipWrap(a.handleSpans))
	mux.HandleFunc("/api/v2/spans", ungzipWrap(a.handleSpans))
	mux.HandleFunc("/", http.NotFoundHandler().ServeHTTP)
	a.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", a.port),
		Handler: mux,
	}
	go a.server.ListenAndServe()
	if len(a.OutputLines) > 0 {
		for _, line := range a.OutputLines {
			fmt.Println(line)
		}
	}
	logrus.WithField("port", a.port).Info("Listening")
	return nil
}

func (a *App) stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return a.server.Shutdown(ctx)
}

// Serve listens for HTTP span requests and prometheus metric requests
// and creates a forwarder suitable for sending augmented spans upstream
func (a *App) Serve() {
	level, err := logrus.ParseLevel(a.logLevel)
	if err != nil {
		logrus.WithField("logLevel", a.logLevel).Warn("Couldn't parse log level - defaulting to Info")
		logrus.SetLevel(logrus.InfoLevel)
	} else {
		logrus.SetLevel(level)
	}
	logrus.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	if a.collectorURL != "" {
		logrus.WithField("collectorURL", a.collectorURL).Debug("Creating trace forwarder")
		a.Forwarder, err = NewForwarder(a.collectorURL)
		if err != nil {
			fmt.Printf("%v", err)
			os.Exit(1)
		}
		a.Forwarder.Start()
		defer a.Forwarder.Stop()
	} else {
		a.Forwarder = nil
	}
	err = a.start()
	defer a.stop()
	if err != nil {
		fmt.Printf("Error starting app: %v\n", err)
		os.Exit(1)
	}

	http.Handle("/metrics", promhttp.Handler())
	http.ListenAndServe(fmt.Sprintf(":%d", a.metricsPort), nil)
	waitForSignal()
}

func waitForSignal() {
	ch := make(chan os.Signal, 1)
	defer close(ch)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(ch)
	<-ch
}
