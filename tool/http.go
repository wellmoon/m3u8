package tool

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/levigross/grequests"
)

var (
	grequestotp = &grequests.RequestOptions{
		// UserAgent:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_13_6) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/79.0.3945.88 Safari/537.36",
		RequestTimeout: 600 * time.Second,
		// Headers: map[string]string{
		// 	"Connection":      "keep-alive",
		// 	"Accept":          "*/*",
		// 	"Accept-Encoding": "*",
		// 	"Accept-Language": "zh-Hans;q=1",
		// },
	}
)

func Get(url string, headers map[string]string) (io.ReadCloser, error) {
	c := http.Client{
		Timeout: time.Duration(60) * time.Second,
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if nil != headers && len(headers) > 0 {
		for key, val := range headers {
			req.Header.Add(key, val)
		}
	}
	resp, err := c.Do(req)
	// resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http error: status code %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func GetBytes(url string, headers map[string]string) ([]byte, error) {
	grequestotp.Headers = headers
	res, err := grequests.Get(url, grequestotp)
	if err != nil {
		fmt.Println("grequest error : ", err)
		return nil, err
	}
	defer res.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("http error: status code %d", res.StatusCode)
	}
	return res.Bytes(), nil
}
