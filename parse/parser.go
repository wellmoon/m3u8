package parse

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/url"
	"strings"

	"github.com/wellmoon/m3u8/tool"
)

type Result struct {
	URL  *url.URL
	M3u8 *M3u8
	Keys map[int]string
}

func FromURL(link string, headers map[string]string, uri *url.URL) (*Result, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	link = u.String()
	body, err := tool.GetByProxy(link, headers, uri)
	if err != nil {
		return nil, fmt.Errorf("request m3u8 URL failed: %s", err.Error())
		// if strings.Contains(err.Error(), "status code 428") && uri != nil {
		// 	// 尝试换代理请求
		// 	fmt.Printf("尝试切换代理请求:%s", uri)
		// 	body, err = tool.GetByProxy(link, headers, uri)
		// 	if err != nil {
		// 		return nil, fmt.Errorf("request m3u8 URL failed: %s", err.Error())
		// 	}
		// } else {
		// 	return nil, fmt.Errorf("request m3u8 URL failed: %s", err.Error())
		// }
	}
	//noinspection GoUnhandledErrorResult
	defer body.Close()
	m3u8, err := parse(body)
	if err != nil {
		return nil, err
	}
	if len(m3u8.MasterPlaylist) != 0 {
		sf := m3u8.MasterPlaylist[0]
		return FromURL(tool.ResolveURL(u, sf.URI), headers, uri)
	}
	if len(m3u8.Segments) == 0 {
		return nil, errors.New("can not found any TS file description")
	}
	result := &Result{
		URL:  u,
		M3u8: m3u8,
		Keys: make(map[int]string),
	}

	for idx, key := range m3u8.Keys {
		switch {
		case key.Method == "" || key.Method == CryptMethodNONE:
			continue
		case key.Method == CryptMethodAES:
			// Request URL to extract decryption key
			keyURL := key.URI
			keyURL = tool.ResolveURL(u, keyURL)
			resp, err := tool.GetByProxy(keyURL, headers, uri)
			if err != nil {
				if strings.Contains(err.Error(), "status code 403") {
					// 如果获取不到key，可能不需要解密
					continue
				} else {
					return nil, fmt.Errorf("extract key failed: %s", err.Error())
				}
			}
			keyByte, err := ioutil.ReadAll(resp)
			_ = resp.Close()
			if err != nil {
				return nil, err
			}
			fmt.Println("decryption key: ", string(keyByte))
			result.Keys[idx] = string(keyByte)
		default:
			return nil, fmt.Errorf("unknown or unsupported cryption method: %s", key.Method)
		}
	}
	return result, nil
}
