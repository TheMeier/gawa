package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"runtime"
	"text/template"
	"time"

	"github.com/Masterminds/sprig"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const supportedWebhookVersion = "4"

var (
	addr           = "localhost:9097"
	application    = "gawa"
	disableChunked = false
	maxClients     = 30
	postTemplate   = "rocketchat2.tmpl"
	targetURL      string
	version        = "undefined"
	versionString  = fmt.Sprintf("%s %s (%s)", application, version, runtime.Version())

	notificationsErrored = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: application,
		Name:      "notifications_errored_total",
		Help:      "Total number of alert notifications that errored during processing and should be retried",
	})
	notificationsInvalid = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: application,
		Name:      "notifications_invalid_total",
		Help:      "Total number of invalid alert notifications received",
	})
	notificationsReceived = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: application,
		Name:      "notifications_received_total",
		Help:      "Total number of alert notifications received",
	})
)

func init() {
	prometheus.MustRegister(notificationsErrored)
	prometheus.MustRegister(notificationsInvalid)
	prometheus.MustRegister(notificationsReceived)
}

func main() {
	// defer profile.Start(profile.MemProfile).Stop()
	var showVersion bool
	flag.StringVar(&addr, "addr", addr, "host:port to listen to")
	flag.StringVar(&targetURL, "targetURL", targetURL, "HTTP URL to post to")
	flag.StringVar(&postTemplate, "postTemplate", postTemplate, "Template for the post content")
	flag.BoolVar(&showVersion, "version", false, "Print version number and exit")
	flag.BoolVar(&disableChunked, "disableChunked", disableChunked, "Disable chunked encondig")
	flag.IntVar(&maxClients, "maxClients", maxClients, "maximum concurrent clients for /webhook")
	flag.Parse()

	if showVersion {
		fmt.Println(versionString)
		os.Exit(0)
	}

	if targetURL == "" {
		fmt.Fprintln(os.Stderr, "Must specify HTTP URL")
		flag.Usage()
		os.Exit(2)
	}

	// test template and panic if it is broken
	template.Must(template.New(path.Base(postTemplate)).Funcs(sprig.TxtFuncMap()).ParseFiles(postTemplate))

	http.DefaultClient.Timeout = 10 * time.Second

	s := &http.Server{
		Addr:         addr,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	http.HandleFunc("/webhook", prometheus.InstrumentHandlerFunc("webhook", maxClientsFunc(handler, maxClients)))
	http.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, versionString)
	})
	http.Handle("/metrics", promhttp.Handler())

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "404: Not found")
		log.Printf("404 when serving path: %s requested by %s", r.RequestURI, r.RemoteAddr)
		return
	})

	log.Print(versionString)
	log.Printf("Listening on %s", addr)
	log.Fatal(s.ListenAndServe())
}

func maxClientsFunc(h http.HandlerFunc, n int) http.HandlerFunc {
	sema := make(chan struct{}, n)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sema <- struct{}{}
		defer func() { <-sema }()

		h.ServeHTTP(w, r)
	})
}

func handler(w http.ResponseWriter, r *http.Request) {
	notificationsReceived.Inc()

	if r.Body == nil {
		notificationsInvalid.Inc()
		err := errors.New("got empty request body")
		http.Error(w, err.Error(), http.StatusBadRequest)
		log.Print(err)
		return
	}

	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		notificationsErrored.Inc()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Print(err)
		return
	}
	defer r.Body.Close()

	var msg notification
	err = json.Unmarshal(b, &msg)
	if err != nil {
		notificationsInvalid.Inc()
		http.Error(w, err.Error(), http.StatusBadRequest)
		log.Print(err)
		return
	}

	if msg.Version != supportedWebhookVersion {
		notificationsInvalid.Inc()
		err := fmt.Errorf("do not understand webhook version %q, only version %q is supported", msg.Version, supportedWebhookVersion)
		http.Error(w, err.Error(), http.StatusBadRequest)
		log.Print(err)
		return
	}

	now := time.Now()
	// ISO8601: https://github.com/golang/go/issues/2141#issuecomment-66058048
	msg.Timestamp = now.Format(time.RFC3339)

	tmpl, err := template.New(path.Base(postTemplate)).Funcs(sprig.TxtFuncMap()).ParseFiles(postTemplate)
	pipeReader, pipeWriter := io.Pipe()
	go func() {
		err = tmpl.Execute(pipeWriter, &msg)
		defer pipeWriter.Close()
	}()

	defer pipeReader.Close()
	var req *http.Request
	if disableChunked {
		var tpl bytes.Buffer
		err = tmpl.Execute(&tpl, &msg)
		req, err = http.NewRequest("POST", targetURL, &tpl)
	} else {
		req, err = http.NewRequest("POST", targetURL, pipeReader)
	}
	if err != nil {
		notificationsErrored.Inc()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Print(err)
		return
	}

	req.Header.Set("User-Agent", versionString)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		notificationsErrored.Inc()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Print(err)
		return
	}

	body, err := ioutil.ReadAll(resp.Body)
	defer resp.Body.Close()
	if err != nil {
		notificationsErrored.Inc()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Print(err)
		return
	}

	if resp.StatusCode/100 != 2 {
		notificationsErrored.Inc()
		err := fmt.Errorf("POST to rest target on %q returned HTTP %d:  %s", targetURL, resp.StatusCode, body)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Print(err)
		return
	}
}

// notification data format from alertmanager
type notification struct {
	Alerts []struct {
		Annotations  map[string]string `json:"annotations"`
		EndsAt       time.Time         `json:"endsAt"`
		GeneratorURL string            `json:"generatorURL"`
		Labels       map[string]string `json:"labels"`
		StartsAt     time.Time         `json:"startsAt"`
		Status       string            `json:"status"`
	} `json:"alerts"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	CommonLabels      map[string]string `json:"commonLabels"`
	ExternalURL       string            `json:"externalURL"`
	GroupLabels       map[string]string `json:"groupLabels"`
	Receiver          string            `json:"receiver"`
	Status            string            `json:"status"`
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`

	// Timestamp records when the alert notification was received
	Timestamp string `json:"@timestamp"`
}
