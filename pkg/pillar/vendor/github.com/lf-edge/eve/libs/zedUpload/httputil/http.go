// Copyright(c) 2017-2018 Zededa, Inc.
// All rights reserved.

package http

import (
	"crypto/tls"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/net/html"
)

const (
	SingleMB int64 = 1024 * 1024
)

type UpdateStats struct {
	Size          int64    // complete size to upload/download
	Asize         int64    // current size uploaded/downloaded
	List          []string //list of images at given path
	Error         error
	BodyLength    int   // Body legth in http response
	ContentLength int64 // Content length in http response
}

type NotifChan chan UpdateStats

var userAgent = "UnityNetworkReporter/" + " (" + runtime.GOOS + " " + runtime.GOARCH + ")"

func getHttpClient() *http.Client {
	tr := &http.Transport{
		TLSNextProto: make(map[string]func(s string, conn *tls.Conn) http.RoundTripper),
	}
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // Do _NOT_ follow redirects!
		},
		Transport: tr,
	}
}

func getHref(token html.Token) (ok bool, href string) {
	// Iterate over all of the Token's attributes until we find an "href"
	for _, attr := range token.Attr {
		if attr.Key == "href" {
			href = attr.Val
			ok = true
		}
	}

	return
}

// ExecCmd performs various commands such as "ls", "get", etc.
// Note that "host" needs to contain the URL in the case of a get
func ExecCmd(cmd, host, remoteFile, localFile string, objSize int64,
	prgNotify NotifChan, client *http.Client) UpdateStats {

	var imgList []string
	stats := UpdateStats{}
	if client == nil {
		client = getHttpClient()
	}
	switch cmd {
	case "ls":
		resp, err := http.Get(host)
		if err != nil {
			stats.Error = fmt.Errorf("get failed for ls %s: %s",
				host, err)
			return stats
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			stats.Error = fmt.Errorf("bad response code for ls %s: %d",
				host, resp.StatusCode)
			return stats
		}

		tokenizer := html.NewTokenizer(resp.Body)

		for tokenType := tokenizer.Next(); tokenType != html.ErrorToken; tokenType = tokenizer.Next() {
			if tokenType == html.StartTagToken {
				token := tokenizer.Token()
				// Check if the token is an <a> tag
				isAnchor := token.Data == "a"
				if !isAnchor {
					continue
				}
				// Extract the href value, if there is one
				ok, url := getHref(token)
				if !ok {
					continue
				}

				imgList = append(imgList, url)
			}
		}

		stats.List = imgList
		if prgNotify != nil {
			select {
			case prgNotify <- stats:
			default: //ignore we cannot write
			}
		}
		return stats
	case "get":
		req, err := http.NewRequest(http.MethodGet, host, nil)
		if err != nil {
			stats.Error = fmt.Errorf("newrequest failed for get %s: %s",
				host, err)
			return stats
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Content-Type", "application/octet-stream")

		resp, err := client.Do(req)
		if err != nil {
			stats.Error = fmt.Errorf("get failed for get %s: %s",
				host, err)
			return stats
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			stats.Error = fmt.Errorf("bad response code for %s: %d",
				host, resp.StatusCode)
			return stats
		}
		tempLocalFile := localFile
		index := strings.LastIndex(tempLocalFile, "/")
		dir := tempLocalFile[:index+1]
		if err = os.MkdirAll(dir, 0755); err != nil {
			stats.Error = fmt.Errorf("cannot create dir %s: %d",
				dir, err)
			return stats
		}
		local, err := os.Create(localFile)
		if err != nil {
			stats.Error = fmt.Errorf("cannot create file %s: %d",
				localFile, err)
			return stats
		}
		defer local.Close()
		chunkSize := SingleMB
		var written int64
		stats.Size = objSize
		for {
			var copyErr error
			if written, copyErr = io.CopyN(local, resp.Body, chunkSize); copyErr != nil && copyErr != io.EOF {
				stats.Error = copyErr
				return stats
			}
			stats.Asize += written //we should process all chunks, not only divisible by chunkSize
			if prgNotify != nil {
				select {
				case prgNotify <- stats:
				default: //ignore we cannot write
				}
			}
			if written != chunkSize {
				// We reached EOF
				break
			}
		}
		stats.BodyLength = int(resp.ContentLength)
		return stats
	case "post":
		file, err := os.Open(localFile)
		if err != nil {
			stats.Error = err
			return stats
		}
		defer file.Close()
		r, w := io.Pipe()
		writer := multipart.NewWriter(w)
		part, err := writer.CreateFormFile(remoteFile, filepath.Base(localFile))
		if err != nil {
			_ = writer.Close()
			_ = w.Close()
			stats.Error = err
			return stats
		}
		go func() {
			_, err := io.Copy(part, file)
			closeErr := writer.Close()
			if err == nil {
				err = closeErr //propagate closeErr
			}
			_ = w.CloseWithError(err) //it always returns nil
		}()
		req, err := http.NewRequest(http.MethodPost, host, r)
		if err != nil {
			stats.Error = fmt.Errorf("newrequest failed for post %s: %s",
				host, err)
			return stats
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Content-Type", writer.FormDataContentType())
		resp, err := client.Do(req)
		if err != nil {
			stats.Error = fmt.Errorf("request failed for post %s: %s",
				host, err)
			return stats
		}
		defer resp.Body.Close()
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			stats.Error = err
			return stats
		}
		stats.Asize = int64(len(body))
		if prgNotify != nil {
			select {
			case prgNotify <- stats:
			default: //ignore we cannot write
			}
		}
		stats.BodyLength = len(body)
		return stats
	case "meta":
		req, err := http.NewRequest(http.MethodHead, host, nil)
		if err != nil {
			stats.Error = fmt.Errorf("request failed for meta %s: %s",
				host, err)
			return stats
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Content-Type", "application/octet-stream")

		resp, err := client.Do(req)
		if err != nil {
			stats.Error = fmt.Errorf("head failed for meta %s: %s",
				host, err)
			return stats
		}
		stats.ContentLength = resp.ContentLength
		return stats
	default:
		stats.Error = fmt.Errorf("unknown subcommand: %v", cmd)
		return stats
	}
}
