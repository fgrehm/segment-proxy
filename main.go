package main

import (
	"bytes"
	"compress/gzip"
	"flag"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/gorilla/handlers"
)

// singleJoiningSlash is copied from httputil.singleJoiningSlash method.
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

// NewSegmentReverseProxy is adapted from the httputil.NewSingleHostReverseProxy
// method, modified to dynamically redirect to different servers (CDN or Tracking API)
// based on the incoming request, and sets the host of the request to the host of of
// the destination URL.
func NewSegmentReverseProxy(cdn *url.URL, trackingAPI *url.URL) http.Handler {
	director := func(req *http.Request) {
		// Figure out which server to redirect to based on the incoming request.
		var target *url.URL
		if strings.HasPrefix(req.URL.String(), "/v1/projects") || strings.HasPrefix(req.URL.String(), "/a.js/v1") || strings.HasPrefix(req.URL.String(), "/analytics.js/v1") {
			target = cdn
		} else {
			target = trackingAPI
		}

		targetQuery := target.RawQuery
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = singleJoiningSlash(target.Path, req.URL.Path)

		if strings.HasPrefix(req.URL.Path, "/a.js/v1") {
			req.URL.Path = strings.Replace(req.URL.Path, "/a.js/v1", "/analytics.js/v1", 1)
		}
		if strings.HasSuffix(req.URL.Path, "a.min.js") {
			req.URL.Path = strings.Replace(req.URL.Path, "a.min.js", "analytics.min.js", 1)
		}

		if targetQuery == "" || req.URL.RawQuery == "" {
			req.URL.RawQuery = targetQuery + req.URL.RawQuery
		} else {
			req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
		}

		// Set the host of the request to the host of of the destination URL.
		// See http://blog.semanticart.com/blog/2013/11/11/a-proper-api-proxy-written-in-go/.
		req.Host = req.URL.Host
	}

	proxy := &httputil.ReverseProxy{Director: director}
	if *customHost != "" {
		proxy.ModifyResponse = func(resp *http.Response) error {
			if !strings.HasPrefix(resp.Request.Host, "cdn.segment.com") {
				return nil
			}
			if resp.StatusCode != http.StatusOK {
				return nil
			}
			return rewriteJs(resp)
		}
	}

	return proxy
}

func rewriteJs(resp *http.Response) error {
	var (
		responseBytes []byte
		err           error
		reader        io.Reader
	)

	if resp.Uncompressed {
		reader = resp.Body
	} else {
		reader, err = gzip.NewReader(resp.Body)
		if err != nil {
			return err
		}
	}

	responseBytes, err = ioutil.ReadAll(reader) // Read response
	if err != nil {
		return err
	}
	err = resp.Body.Close()
	if err != nil {
		return err
	}

	responseBytes = bytes.Replace(responseBytes, []byte("api.segment.io"), []byte(*customHost), -1) // replace html

	if !resp.Uncompressed {
		buf := bytes.Buffer{}
		writer := gzip.NewWriter(&buf)
		_, err := writer.Write(responseBytes)
		if err != nil {
			writer.Close()
			return err
		}
		writer.Close()
		responseBytes = buf.Bytes()
	}

	body := ioutil.NopCloser(bytes.NewReader(responseBytes))
	resp.Body = body
	resp.ContentLength = int64(len(responseBytes))
	resp.Header.Set("Content-Length", strconv.Itoa(len(responseBytes)))

	return nil
}

var port = flag.String("port", "8080", "bind address")
var customHost = flag.String("host", "", "host used for rewriting references to api.segment.io on JS")
var debug = flag.Bool("debug", false, "debug mode")

func main() {
	flag.Parse()
	cdnURL, err := url.Parse("https://cdn.segment.com")
	if err != nil {
		log.Fatal(err)
	}
	trackingAPIURL, err := url.Parse("https://api.segment.io")
	if err != nil {
		log.Fatal(err)
	}

	if *customHost == "" {
		log.Print("WARNING: Custom host was not set, if it is not configured on segment your requests won't go to the proxy")
	}

	proxy := NewSegmentReverseProxy(cdnURL, trackingAPIURL)
	if *debug {
		proxy = handlers.LoggingHandler(os.Stdout, proxy)
		log.Printf("serving proxy at port %v\n", *port)
	}

	log.Fatal(http.ListenAndServe(":"+*port, proxy))
}
