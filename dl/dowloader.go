package dl

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wellmoon/go/utils"
	"github.com/wellmoon/m3u8/parse"
	"github.com/wellmoon/m3u8/tool"
)

const (
	// tsExt            = ".mp4"
	tsFolderName = "ts"
	// mergeTSFilename  = "main.mp4"
	tsTempFileSuffix = "_tmp"
	progressWidth    = 40
)

type Downloader struct {
	lock              sync.Mutex
	queue             []int
	folder            string
	tsFolder          string
	finish            int32
	segLen            int
	headers           map[string]string
	WaterMarker       string
	WaterMarkerWidth  int
	WaterMarkerHeight int
	WaterMarkerLeft   int
	VideoWidth        int
	VideoHeight       int
	WaterMakerType    int // 0.loop  1.fix prefix -1.no mark
	ProxyUrl          string
	UploadFunc        func(fp string)
	ProcessFunc       func(finish int32, total int, u string)
	result            *parse.Result
	CheckTsFunc       func(tsFile string, hkey string, sizeMap map[string]string) bool
	CheckTsKey        string
	CheckTsMap        map[string]string
	AdFileInfo        map[int64]string // key:文件大小；val:md5
	SubTitle          string
	FFmpegPath        string
}

func (d *Downloader) GetExt() string {
	return ".ts"
}

func (d *Downloader) GetFFmpeg() string {
	if len(d.FFmpegPath) == 0 {
		return "ffmpeg"
	}
	return d.FFmpegPath
}

func (d *Downloader) GetMergeFilename() string {
	return "main.ts"
}

func (d *Downloader) GetDuration() int {
	arr := d.result.M3u8.Segments
	duration := 0
	for _, s := range arr {
		duration = duration + int(s.Duration)
	}
	return duration
}

// func (d *Downloader) GetFileSize() int {
// 	arr := d.result.M3u8.Segments
// 	size := 0
// 	for _, s := range arr {
// 		size = size + int(s.Length)
// 	}
// 	return size
// }

// NewTask returns a Task instance
func NewTask(output string, url string, headers map[string]string, uri *url.URL) (*Downloader, error) {
	result, err := parse.FromURL(url, headers, uri)

	if err != nil {
		return nil, err
	}
	var folder string
	// If no output folder specified, use current directory
	if output == "" {
		current, err := tool.CurrentDir()
		if err != nil {
			return nil, err
		}
		folder = filepath.Join(current, output)
	} else {
		folder = output
	}
	if err := os.MkdirAll(folder, os.ModePerm); err != nil {
		return nil, fmt.Errorf("create storage folder failed: %s", err.Error())
	}
	tsFolder := filepath.Join(folder, tsFolderName)
	if err := os.MkdirAll(tsFolder, os.ModePerm); err != nil {
		return nil, fmt.Errorf("create ts folder '[%s]' failed: %s", tsFolder, err.Error())
	}
	d := &Downloader{
		folder:   folder,
		tsFolder: tsFolder,
		result:   result,
		headers:  headers,
	}
	d.segLen = len(result.M3u8.Segments)
	d.queue = genSlice(d.segLen)
	return d, nil
}

// Start runs downloader
func (d *Downloader) Start(concurrency int, parseUrl func(string) string) error {
	var wg sync.WaitGroup
	// struct{} zero size
	limitChan := make(chan struct{}, concurrency)
	for {
		tsIdx, end, err := d.next()
		if err != nil {
			if end {
				break
			}
			continue
		}
		wg.Add(1)
		limitChan <- struct{}{}
		go func(idx int) {
			defer func() {
				wg.Done()
				<-limitChan
			}()
			if err := d.download(idx, parseUrl); err != nil {
				// Back into the queue, retry request
				fmt.Printf("[failed] %s\n", err.Error())
				if strings.HasPrefix(err.Error(), "decryt") {
					return
				}
				if strings.HasPrefix(err.Error(), "no such file or directory") {
					return
				}
				if err := d.back(idx); err != nil {
					fmt.Println(err.Error())
				}

			}
		}(tsIdx)

	}
	wg.Wait()
	if d.UploadFunc != nil {
		// 已上传ts文件，无需合并
		_ = os.RemoveAll(d.tsFolder)
		return nil
	}
	if len(d.WaterMarker) == 0 {
		if err := d.merge(); err != nil {
			return err
		}
	} else {
		// fmt.Println("通过ffmpeg合并")
		if err := d.merge(); err != nil {
			return err
		}
	}

	return nil
}

func (d *Downloader) download(segIndex int, parseUrl func(url string) string) error {
	tsFilename := d.tsFilename(segIndex)

	tsUrl := d.tsURL(segIndex)
	if parseUrl != nil {
		tsUrl = parseUrl(tsUrl)
	}
	if tsUrl == "ad_ts" {
		// 广告，需要过滤掉
		fmt.Println(tsUrl, "is a ad ts, ignore this ts")
		atomic.AddInt32(&d.finish, 1)
		fmt.Printf("[download %6.2f%%] %s\n", float32(d.finish)/float32(d.segLen)*100, tsUrl)
		if d.ProcessFunc != nil {
			d.ProcessFunc(d.finish, d.segLen, tsUrl)
		}
		return nil
	}
	fPath := filepath.Join(d.tsFolder, tsFilename)
	fInfo, err := os.Stat(fPath)
	if err == nil {
		// 判断是否广告
		if d.AdFileInfo != nil && len(d.AdFileInfo) > 0 {
			fsize := fInfo.Size()
			m, ok := d.AdFileInfo[fsize]
			if ok {
				// 判断md5
				f, _ := os.Open(fPath)
				m1 := utils.Md5File(f)
				if m1 == m {
					// 广告
					fmt.Println(tsUrl, "is a ad ts, ignore this ts, fileSize is", fsize)
					atomic.AddInt32(&d.finish, 1)
					fmt.Printf("[download %6.2f%%] %s\n", float32(d.finish)/float32(d.segLen)*100, tsUrl)
					if d.ProcessFunc != nil {
						d.ProcessFunc(d.finish, d.segLen, tsUrl)
					}
					return nil
				}
			}
		}
		// 如果ts存在，校验ts文件是否正确，如果正确，则不再下载
		if d.CheckTsFunc != nil && len(d.CheckTsKey) > 0 {
			if d.CheckTsFunc(fPath, d.CheckTsKey, d.CheckTsMap) {
				// Maybe it will be safer in this way...
				atomic.AddInt32(&d.finish, 1)
				// tool.DrawProgressBar("Downloading", float32(d.finish)/float32(d.segLen), progressWidth)
				fmt.Printf("[download %6.2f%%] %s\n", float32(d.finish)/float32(d.segLen)*100, tsUrl)
				if d.ProcessFunc != nil {
					d.ProcessFunc(d.finish, d.segLen, tsUrl)
				}
				return nil
			} else {
				fmt.Println(fPath, "exist but not correct, need redownload")
			}
		}
	}
	var proxyUri *url.URL
	if len(d.ProxyUrl) > 0 {
		proxyUri, _ = url.Parse(d.ProxyUrl)
	}
	bytes, e := tool.GetBytesByProxy(tsUrl, d.headers, proxyUri)
	if e != nil {
		fmt.Println(e.Error())
		if strings.Contains(e.Error(), "429") {
			time.Sleep(time.Duration(3) * time.Second)
		}
		if strings.Contains(e.Error(), "fatal") {
			atomic.AddInt32(&d.finish, 1)
			return nil
		}
		return fmt.Errorf("request %s, %s", tsUrl, e.Error())
	}
	//noinspection GoUnhandledErrorResult

	fTemp := fPath + tsTempFileSuffix
	f, err := os.Create(fTemp)
	if err != nil {
		return fmt.Errorf("create file: %s, %s", tsFilename, err.Error())
	}
	sf := d.result.M3u8.Segments[segIndex]
	if sf == nil {
		return fmt.Errorf("invalid segment index: %d", segIndex)
	}
	key, ok := d.result.Keys[sf.KeyIndex]
	if ok && key != "" {
		tempBytes, err := tool.AES128Decrypt(bytes, []byte(key),
			[]byte(d.result.M3u8.Keys[sf.KeyIndex].IV), tsUrl)
		if err != nil {
			// return fmt.Errorf("decryt: %s, %s", tsUrl, err.Error())
		} else {
			bytes = tempBytes
		}
	}
	// https://en.wikipedia.org/wiki/MPEG_transport_stream
	// Some TS files do not start with SyncByte 0x47, they can not be played after merging,
	// Need to remove the bytes before the SyncByte 0x47(71).
	syncByte := uint8(71) //0x47
	bLen := len(bytes)
	for j := 0; j < bLen; j++ {
		if bytes[j] == syncByte {
			bytes = bytes[j:]
			break
		}
	}
	w := bufio.NewWriter(f)
	if _, err := w.Write(bytes); err != nil {
		return fmt.Errorf("write to %s: %s", fTemp, err.Error())
	}
	// Release file resource to rename file
	_ = f.Close()
	finfo := Info(d.GetFFmpeg(), fTemp)
	if segIndex == 0 {
		d.VideoWidth = finfo.Width
		d.VideoHeight = finfo.Height
	} else {
		if d.VideoWidth > 0 && d.VideoWidth != finfo.Width || d.VideoHeight > 0 && d.VideoHeight != finfo.Height {
			// 视频大小与第一个不同，可能是广告，需要过滤掉
			atomic.AddInt32(&d.finish, 1)
			return nil
		}
	}
	if err = d.rename(fTemp, fPath, segIndex); err != nil {
		fmt.Println("rename error: ", err.Error())
		// return err
	}

	if d.UploadFunc != nil {
		d.UploadFunc(fPath)
	}

	// Maybe it will be safer in this way...
	atomic.AddInt32(&d.finish, 1)
	//tool.DrawProgressBar("Downloading", float32(d.finish)/float32(d.segLen), progressWidth)
	fmt.Printf("[download %6.2f%%] %s\n", float32(d.finish)/float32(d.segLen)*100, tsUrl)
	if d.ProcessFunc != nil {
		d.ProcessFunc(d.finish, d.segLen, tsUrl)
	}
	return nil
}

func (d *Downloader) rename(fTemp string, fPath string, segIndex int) error {
	// if d.VideoWidth == 0 {
	// 	videoInfo := Info(fTemp)
	// 	d.VideoWidth = videoInfo.Width
	// }
	// if d.VideoWidth <= 1000 {
	// 	return os.Rename(fTemp, fPath)
	// }
	var con bool
	if d.WaterMakerType == 0 {
		con = len(d.WaterMarker) > 0 && (segIndex%100 < 7)
	} else if d.WaterMakerType == 1 {
		con = len(d.WaterMarker) > 0 && segIndex < 3
	} else if d.WaterMakerType == -1 {
		// 不加水印
		con = false
	} else {
		// 全部加水印
		con = true
	}
	if con {
		err := AddWaterMarker(d.GetFFmpeg(), fTemp, fPath, d.WaterMarker, d.WaterMarkerWidth, d.WaterMarkerHeight, d.WaterMarkerLeft)
		if err != nil {
			fmt.Println("add water marker err ", err)
			return err
		}
		os.RemoveAll(fTemp)
		if err == nil {
			return nil
		} else {
			fmt.Println("add water marker error", err)
		}
	}
	return os.Rename(fTemp, fPath)
}

type VideoInfo struct {
	Duration int
	Width    int
	Height   int
	Br       int // 单位是k
}

func TimeToSecond(t string) int {
	if strings.Contains(t, "N/A") {
		return 0
	}
	time := strings.Trim(t, " ")
	hour, _ := strconv.Atoi(time[:2])
	minute, _ := strconv.Atoi(time[3:5])
	second, _ := strconv.Atoi(time[6:8])
	return second + minute*60 + hour*3600
}

func Info(ffmpegPath string, filePath string) *VideoInfo {
	res := &VideoInfo{}
	cmd := exec.Command(ffmpegPath, "-i", filePath)
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return res
	}
	defer stderrPipe.Close()
	if err := cmd.Start(); err != nil {
		return res
	}
	reader := bufio.NewReader(stderrPipe)
	durationReg := regexp.MustCompile(`Duration:(.*?),`)
	fixReg := regexp.MustCompile(`Stream.*?(, \d+x\d+)`)
	for {
		line, _, err := reader.ReadLine()
		if err != nil || err == io.EOF {
			break
		}

		if durationReg.Match(line) {
			arr := durationReg.FindStringSubmatch(string(line))
			res.Duration = TimeToSecond(arr[1])
			if !strings.Contains(string(line), "bitrate") {
				continue
			}
			brStr := RegOneStr(string(line), `bitrate: (.*?) kb/s`)
			br, _ := strconv.Atoi(brStr)
			res.Br = br
			continue
		}
		if fixReg.Match(line) {
			arr := fixReg.FindStringSubmatch(string(line))
			reso := arr[1]
			reso = strings.ReplaceAll(reso, ",", "")
			reso = strings.TrimSpace(reso)
			hw := strings.Split(reso, "x")
			if len(hw) != 2 {
				continue
			}
			width, err := strconv.Atoi(hw[0])
			if err != nil {
				continue
			}
			res.Width = width
			height, err := strconv.Atoi(hw[1])
			if err != nil {
				continue
			}
			res.Height = height
		}
	}
	if err := cmd.Wait(); err != nil {
		return res
	}
	return res
}

func RegOneStr(source string, pattern string) string {
	reg := regexp.MustCompile(pattern)
	if reg.Match([]byte(source)) {
		arr := reg.FindStringSubmatch(source)
		if len(arr) > 1 {
			return arr[1]
		}
	}
	return ""
}

// func AddTextWaterMarker(fTemp string, fPath string, text string) error {
// 	mp4Path := fPath
// 	videoInfo := Info(fTemp)
// 	br := videoInfo.Br
// 	if br == 0 {
// 		br = 1000
// 	}
// 	// ffmpeg -i 0.ts -vf "drawtext=text='电报@TopMovieCN':x=(w-text_w)/2:y=10:fontsize=20:fontcolor=orange:shadowy=2" output.mp4
// 	fmt.Println(br)
// 	err := Cmd(false, "ffmpeg", "-y", "-i", fTemp, "-acodec", "copy", "-b:v", strconv.Itoa(br)+"k", "-vf",
// 		"drawtext=text='"+text+"':x=(w-text_w)/2:y=10:fontsize=20:fontcolor=orange:shadowy=2",
// 		mp4Path)

// 	if err != nil {
// 		fmt.Println("ts to mp4 error", err)
// 		return err
// 	}
// 	// _, err = mp4ToTs(mp4Path)
// 	// return err
// 	return nil
// }

func AddWaterMarker(ffmpegPath string, fTemp string, fPath string, markerPath string, width int, height int, left int) error {
	// dir, fileName := path.Split(fPath)
	// fileName = strings.ReplaceAll(fileName, ".ts", ".mp4")
	// mp4Path := filepath.Join(dir, fileName)
	if left == 0 {
		left = 10
	}
	mp4Path := fPath
	videoInfo := Info(ffmpegPath, fTemp)
	br := videoInfo.Br
	if br == 0 {
		br = 1000
	}
	// if width <= 1280 {
	// 	width = 600
	// 	height = 30
	// }
	// err := Cmd(false, "ffmpeg", "-y", "-i", fTemp, "-c:v", "libx264", "-b:v", strconv.Itoa(br)+"k", "-bufsize", strconv.Itoa(br)+"k", "-c:a", "copy", "-vf", "movie="+markerPath+"[watermark];[in][watermark] overlay=10:10[out]", mp4Path)
	// ultrafast, superfast, veryfast, faster, fast, medium, slow, slower, veryslow, placebo
	// err := Cmd(false, "ffmpeg", "-y", "-i", fTemp, "-i", markerPath, "-c:v", "libx264", "-b:v", strconv.Itoa(br)+"k", "-bufsize",
	// 	strconv.Itoa(br)+"k", "-c:a", "copy", "-filter_complex", "[1:v] scale="+strconv.Itoa(width)+":"+strconv.Itoa(height)+" [logo];[0:v][logo]overlay=x=10:y=10",
	// 	"-threads", strconv.Itoa(runtime.NumCPU()), "-preset",
	// 	"superfast",
	// 	mp4Path)
	// brStr := strconv.Itoa(br)
	err := Cmd(false, ffmpegPath, "-y", "-i", fTemp, "-i", markerPath, "-c:v", "libx264",
		"-c:a", "copy", "-filter_complex", "[1:v] scale="+strconv.Itoa(width)+":"+strconv.Itoa(height)+" [logo];[0:v][logo]overlay=x="+strconv.Itoa(left)+":y=10",
		"-threads", strconv.Itoa(runtime.NumCPU()), "-preset",
		"superfast",
		mp4Path)

	if err != nil {
		fmt.Println("ts to mp4 error", err)
		return err
	}
	// _, err = mp4ToTs(mp4Path)
	// return err
	return nil
}

func mp4ToTs(ffmpegPath string, filePath string) (string, error) {
	// ffmpeg -i javtvm.mp4 -c:v copy javtvm.ts
	dir, fileName := path.Split(filePath)
	fileName = strings.ReplaceAll(fileName, ".mp4", ".ts")
	tsPath := filepath.Join(dir, fileName)
	err := Cmd(false, ffmpegPath, "-i", filePath, "-c:v", "copy", tsPath)
	if err != nil {
		return "", err
	}
	os.Remove(filePath)
	return tsPath, nil
}

func Cmd(showDetail bool, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	// 将标准输出和标准错误输出设为nil，以避免任何输出
	cmd.Stdout = nil
	cmd.Stderr = nil

	// 执行命令
	err := cmd.Run()
	if err != nil {
		return err
	}
	return nil
	// stderrPipe, err := cmd.StderrPipe()
	// if err != nil {
	// 	return err
	// }
	// defer stderrPipe.Close()
	// if err := cmd.Start(); err != nil {
	// 	return err
	// }
	// reader := bufio.NewReader(stderrPipe)
	// for {
	// 	line, err := reader.ReadBytes('\r')
	// 	if err != nil || err == io.EOF {
	// 		break
	// 	}
	// 	if showDetail {
	// 		fmt.Println(string(line))
	// 	}

	// }
	// if err := cmd.Wait(); err != nil {
	// 	return nil
	// }
	// return nil
}

func (d *Downloader) next() (segIndex int, end bool, err error) {
	d.lock.Lock()
	defer d.lock.Unlock()
	if len(d.queue) == 0 {
		err = fmt.Errorf("queue empty")
		if d.finish == int32(d.segLen) {
			end = true
			return
		}
		// Some segment indexes are still running.
		end = false
		return
	}
	segIndex = d.queue[0]
	d.queue = d.queue[1:]
	return
}

func (d *Downloader) back(segIndex int) error {
	d.lock.Lock()
	defer d.lock.Unlock()
	if sf := d.result.M3u8.Segments[segIndex]; sf == nil {
		return fmt.Errorf("invalid segment index: %d", segIndex)
	}
	d.queue = append(d.queue, segIndex)
	return nil
}

func (d *Downloader) merge() error {
	// In fact, the number of downloaded segments should be equal to number of m3u8 segments
	missingCount := 0
	for idx := 0; idx < d.segLen; idx++ {
		tsFilename := d.tsFilename(idx)
		f := filepath.Join(d.tsFolder, tsFilename)
		if _, err := os.Stat(f); err != nil {
			missingCount++
		}
	}
	if missingCount > 0 {
		fmt.Printf("[warning] %d files missing\n", missingCount)
	}

	// Create a TS file for merging, all segment files will be written to this file.
	mFilePath := filepath.Join(d.folder, d.GetMergeFilename())
	mFile, err := os.Create(mFilePath)
	if err != nil {
		return fmt.Errorf("create main TS file failed：%s", err.Error())
	}
	//noinspection GoUnhandledErrorResult
	defer mFile.Close()

	writer := bufio.NewWriter(mFile)
	mergedCount := 0
	for segIndex := 0; segIndex < d.segLen; segIndex++ {
		tsFilename := d.tsFilename(segIndex)
		bytes, err := ioutil.ReadFile(filepath.Join(d.tsFolder, tsFilename))
		if err != nil {
			fmt.Printf("read files %d error, err is %s\n ", segIndex, err)
			continue
		}
		_, err = writer.Write(bytes)
		if err != nil {
			fmt.Printf("write files %d error, err is %s\n ", segIndex, err)
			continue
		}
		os.Remove(tsFilename)
		mergedCount++
		tool.DrawProgressBar("merge",
			float32(mergedCount)/float32(d.segLen), progressWidth)
	}
	_ = writer.Flush()

	if mergedCount != d.segLen {
		fmt.Printf("[warning] \n%d files merge failed", d.segLen-mergedCount)
		// return errors.New("merge failded")
	}
	// Remove `ts` folder
	_ = os.RemoveAll(d.tsFolder)
	fmt.Printf("\n[output] %s\n", mFilePath)

	return nil
}

func (d *Downloader) mergeByFfmpeg() error {
	// In fact, the number of downloaded segments should be equal to number of m3u8 segments
	missingCount := 0
	for idx := 0; idx < d.segLen; idx++ {
		tsFilename := d.tsFilename(idx)
		f := filepath.Join(d.tsFolder, tsFilename)
		if _, err := os.Stat(f); err != nil {
			missingCount++
		}
	}
	if missingCount > 0 {
		fmt.Printf("[warning] %d files missing\n", missingCount)
	}

	// Create a TS file for merging, all segment files will be written to this file.
	mFilePath := filepath.Join(d.folder, d.GetMergeFilename())

	var in = make([]string, 0)
	for segIndex := 0; segIndex < d.segLen; segIndex++ {

		tsFilename := d.tsFilename(segIndex)
		indexFile := filepath.Join(d.tsFolder, tsFilename)
		in = append(in, indexFile)
	}
	fmt.Println("in", in)
	videoMerge(d.GetFFmpeg(), in, mFilePath)
	// Remove `ts` folder
	_ = os.RemoveAll(d.tsFolder)
	fmt.Printf("\n[output] %s\n", mFilePath)

	return nil
}

func videoMerge(ffmpegPath string, in []string, out string) {
	//fmt.Println(in, out)
	cmdStr := fmt.Sprintf("%s -i concat:%s -acodec copy -vcodec copy -absf aac_adtstoasc %s",
		ffmpegPath, strings.Join(in, "|"), out)
	args := strings.Split(cmdStr, " ")
	msg, err := CmdArr(args[0], args[1:])
	if err != nil {
		fmt.Printf("videoMerge failed, %v, output: %v\n", err, msg)
		return
	}
}

func CmdArr(commandName string, params []string) (string, error) {
	cmd := exec.Command(commandName, params...)
	//fmt.Println("Cmd", cmd.Args)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	err := cmd.Start()
	if err != nil {
		return "", err
	}
	err = cmd.Wait()
	return out.String(), err
}

func (d *Downloader) tsURL(segIndex int) string {
	seg := d.result.M3u8.Segments[segIndex]
	return tool.ResolveURL(d.result.URL, seg.URI)
}

func (d *Downloader) tsFilename(ts int) string {
	return strconv.Itoa(ts) + d.GetExt()
}

func genSlice(len int) []int {
	s := make([]int, 0)
	for i := 0; i < len; i++ {
		s = append(s, i)
	}
	return s
}
