// This file is entirely based on honeycomb-opentracing-proxy's
// Mirror class

package processor

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"sync"

	"github.com/sirupsen/logrus"
)

type Payload struct {
	ContentType string
	Body        []byte
}

// Forwarder sends traffic to a DownstreamURL
type Forwarder struct {
	DownstreamURL  *url.URL
	BufSize        int
	MaxConcurrency int

	payloads chan Payload
	stopped  bool
	wg       sync.WaitGroup
}

func (f *Forwarder) Start() error {
	if f.MaxConcurrency == 0 {
		f.MaxConcurrency = 100
	}
	if f.BufSize == 0 {
		f.BufSize = 4096
	}
	f.payloads = make(chan Payload, f.BufSize)
	for i := 0; i < f.MaxConcurrency; i++ {
		f.wg.Add(1)
		go f.runWorker()
	}
	return nil
}

func (f *Forwarder) Stop() error {
	f.stopped = true
	if f.payloads == nil {
		return nil
	}
	close(f.payloads)
	f.wg.Wait()
	return nil
}

func (f *Forwarder) runWorker() {
	for p := range f.payloads {
		r, err := http.NewRequest("POST", f.DownstreamURL.String(), bytes.NewReader(p.Body))
		r.Header.Set("Content-Type", p.ContentType)
		if err != nil {
			logrus.WithError(err).Info("Error building downstream request")
			return
		}
		client := &http.Client{}
		resp, err := client.Do(r)
		if err != nil {
			logrus.WithError(err).Info("Error sending payload downstream")
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			responseBody, _ := ioutil.ReadAll(&io.LimitedReader{R: resp.Body, N: 1024})
			logrus.WithField("status", resp.Status).
				WithField("response", string(responseBody)).
				Info("Error response sending payload downstream")
			logrus.WithField("payload", string(p.Body)).Debug("Error response sending payload downstream")
		}
	}
	f.wg.Done()
}

func (f *Forwarder) Send(p Payload) error {
	if f.stopped {
		return errors.New("sink stopped")
	}
	select {
	case f.payloads <- p:
		return nil
	default:
		return errors.New("sink full")
	}
}

func NewForwarder(collector string) (*Forwarder, error) {
	downstreamURL, err := url.Parse(collector)
	if err != nil {
		return nil, fmt.Errorf("invalid downstream url %s", collector)
	}

	scheme := downstreamURL.Scheme
	isHTTP := scheme == "http" || scheme == "https"
	if !isHTTP {
		return nil, fmt.Errorf("invalid downstream url %s. Must be prefixed with http:// or https://", collector)
	}

	downstreamURL.Path = "/api/v1/spans"
	forwarder := new(Forwarder)
	forwarder.DownstreamURL = downstreamURL
	return forwarder, nil
}
