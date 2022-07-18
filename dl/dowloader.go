package dl

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
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
	VideoWidth        int

	result *parse.Result
}

func (d *Downloader) GetExt() string {
	return ".ts"
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
func NewTask(output string, url string, headers map[string]string) (*Downloader, error) {
	result, err := parse.FromURL(url, headers)
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
				// fmt.Printf("[failed] %s\n", err.Error())
				if err := d.back(idx); err != nil {
					fmt.Println(err.Error())
				}
			}
		}(tsIdx)

	}
	wg.Wait()
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
	bytes, e := tool.GetBytes(tsUrl, d.headers)
	if e != nil {
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
	fPath := filepath.Join(d.tsFolder, tsFilename)
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
		bytes, err = tool.AES128Decrypt(bytes, []byte(key),
			[]byte(d.result.M3u8.Keys[sf.KeyIndex].IV), tsUrl)
		if err != nil {
			return fmt.Errorf("decryt: %s, %s", tsUrl, err.Error())
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
	if err = d.rename(fTemp, fPath, segIndex); err != nil {
		return err
	}

	// Maybe it will be safer in this way...
	atomic.AddInt32(&d.finish, 1)
	//tool.DrawProgressBar("Downloading", float32(d.finish)/float32(d.segLen), progressWidth)
	fmt.Printf("[download %6.2f%%] %s\n", float32(d.finish)/float32(d.segLen)*100, tsUrl)
	return nil
}

func (d *Downloader) rename(fTemp string, fPath string, segIndex int) error {
	if d.VideoWidth == 0 {
		videoInfo := Info(fTemp)
		d.VideoWidth = videoInfo.Width
		fmt.Println("video width ", d.VideoWidth)
	}
	if d.VideoWidth <= 640 {
		return os.Rename(fTemp, fPath)
	}
	if len(d.WaterMarker) > 0 && (segIndex%100 < 10) {
		fmt.Println("need add water marker", segIndex)
		err := AddWaterMarker(fTemp, fPath, d.WaterMarker, d.WaterMarkerWidth, d.WaterMarkerHeight)
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

func Info(filePath string) *VideoInfo {
	res := &VideoInfo{}
	cmd := exec.Command("ffmpeg", "-i", filePath)
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

func AddWaterMarker(fTemp string, fPath string, markerPath string, width int, height int) error {
	// dir, fileName := path.Split(fPath)
	// fileName = strings.ReplaceAll(fileName, ".ts", ".mp4")
	// mp4Path := filepath.Join(dir, fileName)
	mp4Path := fPath
	videoInfo := Info(fTemp)
	br := videoInfo.Br
	if br == 0 {
		br = 1000
	}
	if width == 0 {
		width = 200
	}
	if height == 0 {
		height = 100
	}
	// err := Cmd(false, "ffmpeg", "-y", "-i", fTemp, "-c:v", "libx264", "-b:v", strconv.Itoa(br)+"k", "-bufsize", strconv.Itoa(br)+"k", "-c:a", "copy", "-vf", "movie="+markerPath+"[watermark];[in][watermark] overlay=10:10[out]", mp4Path)
	// ultrafast, superfast, veryfast, faster, fast, medium, slow, slower, veryslow, placebo
	err := Cmd(false, "ffmpeg", "-y", "-i", fTemp, "-i", markerPath, "-c:v", "libx264", "-b:v", strconv.Itoa(br)+"k", "-bufsize",
		strconv.Itoa(br)+"k", "-c:a", "copy", "-filter_complex", "[1:v] scale="+strconv.Itoa(width)+":"+strconv.Itoa(height)+" [logo];[0:v][logo]overlay=x=10:y=10",
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

func mp4ToTs(filePath string) (string, error) {
	// ffmpeg -i javtvm.mp4 -c:v copy javtvm.ts
	dir, fileName := path.Split(filePath)
	fileName = strings.ReplaceAll(fileName, ".mp4", ".ts")
	tsPath := filepath.Join(dir, fileName)
	err := Cmd(false, "ffmpeg", "-i", filePath, "-c:v", "copy", tsPath)
	if err != nil {
		return "", err
	}
	os.Remove(filePath)
	return tsPath, nil
}

func Cmd(showDetail bool, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	defer stderrPipe.Close()
	if err := cmd.Start(); err != nil {
		return err
	}
	reader := bufio.NewReader(stderrPipe)
	for {
		line, err := reader.ReadBytes('\r')
		if err != nil || err == io.EOF {
			break
		}
		if showDetail {
			fmt.Println(string(line))
		}

	}
	if err := cmd.Wait(); err != nil {
		return nil
	}
	return nil
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
		return errors.New("merge failded")
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
	videoMerge(in, mFilePath)
	// Remove `ts` folder
	_ = os.RemoveAll(d.tsFolder)
	fmt.Printf("\n[output] %s\n", mFilePath)

	return nil
}

func videoMerge(in []string, out string) {
	//fmt.Println(in, out)
	cmdStr := fmt.Sprintf("ffmpeg -i concat:%s -acodec copy -vcodec copy -absf aac_adtstoasc %s",
		strings.Join(in, "|"), out)
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
