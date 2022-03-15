package tool

import (
	"errors"
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
	c := http.Client{
		Timeout: time.Duration(300) * time.Second,
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
		fmt.Println("c.Do err", err)
		return nil, err
	}
	if resp == nil {
		resp.Body.Close()
		return nil, errors.New("fatal, resp is nil")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http error: status code %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	// 用ioutil.ReadAll可能户内存溢出，自己重写ReadAll方法，把之前的512改为256
	bytes, err := ReadAll(resp.Body)
	// var buf bytes.Buffer
	// fmt.Println("start io copy")
	// _, err = io.Copy(&buf, resp.Body)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

func ReadAll(r io.Reader) ([]byte, error) {
	b := make([]byte, 0, 256)
	for {
		if len(b) == cap(b) {
			// Add more capacity (let append pick how much).
			b = append(b, 0)[:len(b)]
		}
		n, err := r.Read(b[len(b):cap(b)])
		b = b[:len(b)+n]
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return b, err
		}
	}
}

// func GetBytes(url string, headers map[string]string) ([]byte, error) {
// 	grequestotp.Headers = headers
// 	res, err := grequests.Get(url, grequestotp)
// 	if err != nil {
// 		fmt.Println("grequest error : ", err)
// 		return nil, err
// 	}
// 	defer res.Close()
// 	if res.StatusCode != 200 {
// 		return nil, fmt.Errorf("http error: status code %d", res.StatusCode)
// 	}
// 	return res.Bytes(), nil
// }
