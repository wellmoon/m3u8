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

func Get(url string) (io.ReadCloser, error) {
	c := http.Client{
		Timeout: time.Duration(60) * time.Second,
	}
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http error: status code %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func GetBytes(url string) ([]byte, error) {
	res, err := grequests.Get(url, grequestotp)
	if err != nil {
		fmt.Println("grequest error")
		return nil, err
	}
	return res.Bytes(), nil
}
