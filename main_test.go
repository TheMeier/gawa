package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestHappyPath(t *testing.T) {
	var (
		request     *http.Request
		requestBody []byte
	)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error

		if r.Body == nil {
			t.Fatal("got empty response body")
			return
		}
		requestBody, err = ioutil.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		request = r

		io.WriteString(w, "OK\n")
	}))
	defer s.Close()

	targetURL = s.URL
	w, r := recordedRequest(t, s.URL+"/webhook")
	handler(w, r)

	expectedCode := http.StatusOK
	if w.Code != expectedCode {
		t.Fatalf("expected HTTP status %d, got %d", expectedCode, w.Code)
	}

	u, _ := url.Parse(s.URL)
	expected := u.Host
	if request.Host != expected {
		t.Fatalf("expected request host %s, got %s", expected, request.Host)
	}

	expected = "POST"
	if request.Method != expected {
		t.Fatalf("expected request method %s, got %s", expected, request.Method)
	}

	// the timestamp changes every second, making it hard to test, so strip it off before comparing
	// lengthOfTimestamp := 42
	l := len(postContent)
	fmt.Printf("sent %s", requestBody)

	if !bytes.Equal(postContent[:l], requestBody[:l]) {
		t.Fatalf("expected payload %q, got %q", postContent[:l], requestBody[:l])
	}
}

func TestErrorsPassedThrough(t *testing.T) {
	// Mock Rest server returns 404s
	s := httptest.NewServer(http.NotFoundHandler())
	defer s.Close()

	targetURL = s.URL
	w, r := recordedRequest(t, s.URL+"/webhook")
	handler(w, r)

	expectedCode := http.StatusInternalServerError
	if w.Code != expectedCode {
		t.Fatalf("expected HTTP status %d, got %d", expectedCode, w.Code)
	}
}

func recordedRequest(t *testing.T, url string) (*httptest.ResponseRecorder, *http.Request) {
	w := httptest.NewRecorder()
	r, err := http.NewRequest("POST", url, bytes.NewBuffer(amNotification))
	if err != nil {
		t.Fatal(err)
	}

	return w, r
}

var amNotification = []byte(`{
	"alerts": [
		{
			"annotations": {
				"link": "https://example.com/Foo+Bar",
				"summary": "Alert summary"
			},
			"endsAt": "0001-01-01T00:00:00Z",
			"generatorURL": "https://example.com",
			"labels": {
				"alertname": "Foo_Bar",
				"instance": "foo1"
			},
			"startsAt": "2017-02-02T16:51:13.507955756Z",
			"status": "firing"
		},
		{
			"annotations": {
				"link": "https://example.com/Foo+Bar",
				"summary": "Alert summary"
			},
			"endsAt": "0001-01-01T00:00:00Z",
			"generatorURL": "https://example.com",
			"labels": {
				"alertname": "Foo_Bar",
				"source": "foo2-source"
			},
			"startsAt": "2017-02-02T16:51:13.507955756Z",
			"status": "firing"
		},
		{
			"annotations": {
				"link": "https://example.com/Foo+Bar",
				"summary": "Alert summary2"
			},
			"endsAt": "0001-01-01T01:00:00Z",
			"generatorURL": "https://example.com",
			"labels": {
				"alertname": "Foo_Bar",
				"instance": "foo3"
			},
			"startsAt": "2017-02-02T16:52:13.507955756Z",
			"status": "resolved"
		}
	],
	"commonAnnotations": {
		"link": "https://example.com/Foo+Bar",
		"summary": "Alert summary",
    "description": "alert description"
	},
	"commonLabels": {
		"alertname": "Foo_Bar",
		"app": "testapp",
		"job": "testjob"
	},
	"externalURL": "https://alertmanager.example.com",
	"groupLabels": {
		"alertname": "Foo_Bar",
		"app": "testapp",
		"job": "testjob"
	},
	"receiver": "alertmanager2es",
	"status": "firing",
	"version": "4",
	"groupKey": "{}/{}/{notify=\"default\":{alertname=\"Foo_Bar\", instance=\"foo\"}"
}`)

var postContent = []byte(`
{
  "text": "#### :exclamation: [FIRING: Foo_Bar](https://example.com)\n\t *Description*: alert description\n\t *Instances*: `+"`foo1` `foo2-source`"+`\n\t *Labels*: alertname: `+"`"+`Foo_Bar`+"`"+` app: `+"`"+`testapp`+"`"+` job: `+"`"+`testjob`+"`"+`"
}
`)
